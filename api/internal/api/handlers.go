package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

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

// handleHealth, `GET /healthz`.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleListIdeas, `GET /api/ideas?source_type=&q=&limit=`.
func (s *Server) handleListIdeas(w http.ResponseWriter, r *http.Request) {
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

// handleGetIdea, `GET /api/ideas/{id}`. Geçersiz id de 404 döner.
func (s *Server) handleGetIdea(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}

	idea, err := s.ideas.GetIdea(r.Context(), id)
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

// handleIdeaSources, `GET /api/ideas/{id}/sources`. Kart yoksa 404.
func (s *Server) handleIdeaSources(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}

	// Kart var mı önce doğrulanır — kaynak listesi boş dönebileceğinden
	// (kartın kendisi yoksa) 404/200-boş-liste ayrımı buradan gelir.
	if _, err := s.ideas.GetIdea(r.Context(), id); err != nil {
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

// handleNotFound, tanımlı olmayan yollar (ve GET dışı metodlar).
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found")
}
