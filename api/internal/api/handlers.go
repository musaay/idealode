package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/musaay/idealode/api/internal/copilot"
	"github.com/musaay/idealode/api/internal/store"
)

// knownSourceTypes, api sözleşmesinde tanınan kaynak türleri. web paketinin
// aynı adlı haritasıyla birebir aynı tutulmalı (iki paket birbirine
// bağımlı değil, bilinçli tekrar).
var knownSourceTypes = map[string]bool{
	"pain_point":     true,
	"market_derived": true,
	"ai_blended":     true,
	"ai_generated":   true,
	"user_created":   true,
}

// maxQueryRunes, q parametresinin kırpıldığı üst sınır.
const maxQueryRunes = 200

// maxChatMessageRunes, tek bir sohbet mesajının üst sınırı (sözleşme).
const maxChatMessageRunes = 1000

// maxChatDisplay, GET /chat'in döndürdüğü en fazla mesaj sayısı (LLM
// bağlamına giren copilot.MaxHistoryWindow'dan AYRI — görüntüleme daha
// geniş bir pencere gösterebilir).
const maxChatDisplay = 200

// sessionIDMinBytes/MaxBytes: X-Session-Id başlığının hex-decode edilmiş
// uzunluk sınırları ("biçimsiz" -> 400). serve 32 bayt üretir; sınırlar
// gelecekte format değişse de savunmacı kalsın diye biraz gevşek tutulur.
const (
	sessionIDMinBytes = 8
	sessionIDMaxBytes = 128
)

// logHata, beklenmeyen store hatalarını loglar. Zaman aşımı sonrası
// context'in iptal olması (r.Context().Err() != nil) handler için de normal
// bir hata yolu üretir — bu durumda tekrar loglamak yalnız gürültüdür,
// istemciye zaten timeoutWriter üzerinden 503 gitmiştir.
func logHata(r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	log.Printf("hata: %s %s: %v", r.Method, r.URL.Path, err)
}

// sessionIDFromRequest, X-Session-Id başlığını ayrıştırır. Başlık yoksa
// veya hex biçiminde değilse (ya da uzunluk sınırları dışındaysa) ok=false
// döner — çağıran bunu 400 bad_request'e çevirir.
func sessionIDFromRequest(r *http.Request) (string, bool) {
	sid := strings.TrimSpace(r.Header.Get("X-Session-Id"))
	if sid == "" {
		return "", false
	}
	raw, err := hex.DecodeString(sid)
	if err != nil || len(raw) < sessionIDMinBytes || len(raw) > sessionIDMaxBytes {
		return "", false
	}
	return sid, true
}

// requireSessionID, sid'i çıkarır; geçersizse 400 yazıp false döner.
func requireSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	sid, ok := sessionIDFromRequest(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request")
		return "", false
	}
	return sid, true
}

// parseIdeaID, path'teki {id}'yi ayrıştırır; geçersizse 404 yazıp false
// döner (mevcut olmayan kaynak ile aynı davranış — id sızdırmaz).
func parseIdeaID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return 0, false
	}
	return id, true
}

// handleHealth, `GET /healthz`.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleListIdeas, `GET /api/ideas?source_type=&q=&limit=`.
func (s *Server) handleListIdeas(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if rq := []rune(q); len(rq) > maxQueryRunes {
		q = string(rq[:maxQueryRunes])
	}

	sourceType := strings.TrimSpace(r.URL.Query().Get("source_type"))
	if sourceType != "" && !knownSourceTypes[sourceType] {
		// Bilinmeyen tür sessizce boş liste döner; sorguya ham değer gitmez.
		writeJSON(w, http.StatusOK, map[string]any{"ideas": []store.Idea{}})
		return
	}

	// limit geçersiz/boşsa 0'a düşer; ListIdeasFiltered <=0 -> 60,
	// >200 -> 200 kuralını zaten uyguluyor.
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))

	ideas, err := s.ideas.ListIdeasFiltered(r.Context(), store.IdeaFilter{
		SourceType: sourceType,
		Query:      q,
		Limit:      limit,
		SessionID:  sid,
	})
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if ideas == nil {
		ideas = []store.Idea{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ideas": ideas})
}

// handleGetIdea, `GET /api/ideas/{id}`. Geçersiz id de 404 döner. Başkasının
// ai_blended kartı da store katmanında ErrNotFound'a çevrilir (#66).
func (s *Server) handleGetIdea(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}
	id, ok := parseIdeaID(w, r)
	if !ok {
		return
	}

	idea, err := s.ideas.GetIdea(r.Context(), id, sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"idea": idea})
}

// handleIdeaSources, `GET /api/ideas/{id}/sources`. Kart yoksa (ya da
// başkasının ai_blended kartıysa) 404.
func (s *Server) handleIdeaSources(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}
	id, ok := parseIdeaID(w, r)
	if !ok {
		return
	}

	// Kart var mı (ve görünür mü) önce doğrulanır — kaynak listesi boş
	// dönebileceğinden 404/200-boş-liste ayrımı buradan gelir.
	if _, err := s.ideas.GetIdea(r.Context(), id, sid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	sources, err := s.ideas.IdeaSources(r.Context(), id)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if sources == nil {
		sources = []store.IdeaSource{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

// handleGetChat, `GET /api/ideas/{id}/chat`. (kart, oturum) çiftinin
// geçmişini kronolojik sırada döner (boşsa []).
func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}
	id, ok := parseIdeaID(w, r)
	if !ok {
		return
	}

	if _, err := s.ideas.GetIdea(r.Context(), id, sid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	messages, err := s.ideas.ListChat(r.Context(), id, sid, maxChatDisplay)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if messages == nil {
		messages = []store.ChatMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

// chatRequestBody, POST /chat gövdesi.
type chatRequestBody struct {
	Message string `json:"message"`
	Lang    string `json:"lang"`
}

// blendRequestBody, POST /blend gövdesi.
type blendRequestBody struct {
	Lang string `json:"lang"`
}

// normalizeLang, sözleşmedeki tr|en dışında bir değer (ya da boş) gelirse
// "tr" varsayılanına düşer (OUTPUT_LANG varsayılanıyla tutarlı).
func normalizeLang(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "en":
		return "en"
	default:
		return "tr"
	}
}

// decodeJSONBody, gövdeyi savunmacı ayrıştırır: tamamen boş gövde (io.EOF)
// hata SAYILMAZ (alanlar sıfır değerinde kalır — blend'in gövdesiz
// çağrılabilmesi için); bozuk JSON hata döner.
func decodeJSONBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// handlePostChat, `POST /api/ideas/{id}/chat`.
func (s *Server) handlePostChat(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}
	id, ok := parseIdeaID(w, r)
	if !ok {
		return
	}

	var body chatRequestBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" || len([]rune(message)) > maxChatMessageRunes {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}

	idea, err := s.ideas.GetIdea(r.Context(), id, sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	if !s.chatLimiter.Allow(sid) {
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}

	history, err := s.ideas.ListChat(r.Context(), id, sid, copilot.MaxHistoryWindow)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	result, err := copilot.Chat(r.Context(), s.chat, idea, history, message, normalizeLang(body.Lang))
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusBadGateway, "upstream")
		return
	}

	// Kullanıcı mesajı + asistan cevabı sohbete yazılır — yalnız BAŞARILI
	// LLM turundan sonra (502 durumunda kullanıcı mesajı kaydedilmez ki
	// istemci tekrar denediğinde geçmişte yinelenmesin).
	if _, err := s.ideas.AppendChat(r.Context(), id, sid, "user", message); err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	replyMsg, err := s.ideas.AppendChat(r.Context(), id, sid, "assistant", result.Reply)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reply":       replyMsg,
		"suggestions": result.Suggestions,
	})
}

// handlePostBlend, `POST /api/ideas/{id}/blend`.
func (s *Server) handlePostBlend(w http.ResponseWriter, r *http.Request) {
	sid, ok := requireSessionID(w, r)
	if !ok {
		return
	}
	id, ok := parseIdeaID(w, r)
	if !ok {
		return
	}

	var body blendRequestBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}

	idea, err := s.ideas.GetIdea(r.Context(), id, sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	history, err := s.ideas.ListChat(r.Context(), id, sid, copilot.MaxHistoryWindow)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if len(history) == 0 {
		writeError(w, http.StatusConflict, "no_conversation")
		return
	}

	if !s.blendLimiter.Allow(sid) {
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}

	draft, err := copilot.Blend(r.Context(), s.chat, idea, history, normalizeLang(body.Lang))
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusBadGateway, "upstream")
		return
	}

	newIdea, err := s.ideas.InsertBlendedIdea(r.Context(), idea, draft, sid)
	if err != nil {
		logHata(r, err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"idea": newIdea})
}

// handleNotFound, tanımlı olmayan yollar (ve GET dışı metodlar).
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found")
}
