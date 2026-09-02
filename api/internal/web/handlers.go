package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/musaay/idealode/api/internal/store"
)

// handleHealth, Railway sağlık kontrolü. Veritabanına gitmez — uygulama
// ayakta mı sorusunu yanıtlar, bağımlılık durumunu değil.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleStatic, gömülü CSS/JS'i sunar. İçerik sürüm damgasıyla istendiği
// için uzun cache güvenlidir.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	s.static.ServeHTTP(w, r)
}

// handleGallery, `GET /` — filtre + arama ile kart listesi.
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	base := s.newPage(w, r)

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 120 {
		q = string([]rune(q)[:120])
	}
	sourceType := strings.TrimSpace(r.URL.Query().Get("source_type"))
	if sourceType != "" && !knownSourceTypes[sourceType] {
		// Bilinmeyen tür sessizce yok sayılır; sorguya ham değer gitmez.
		sourceType = ""
	}

	ideas, err := s.ideas.ListIdeasFiltered(r.Context(), store.IdeaFilter{
		SourceType: sourceType,
		Query:      q,
	})
	if err != nil {
		s.renderUpstreamError(w, r, base, err)
		return
	}

	page := buildGallery(base, ideas, sourceType, q)
	page.Title = page.T("gallery.title") + " — " + page.T("app.name")
	page.MobileTitle = page.T("app.name")
	page.NavCount = strconv.Itoa(page.Count)
	page.OnGallery = true
	s.render(w, r, "gallery", http.StatusOK, page)
}

// handleIdea, `GET /ideas/{id}` — kart detayı. Geçersiz id ve bulunamayan
// kart aynı şekilde 404 sayfasına düşer.
func (s *Server) handleIdea(w http.ResponseWriter, r *http.Request) {
	base := s.newPage(w, r)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.renderNotFound(w, r, base)
		return
	}

	idea, err := s.ideas.GetIdea(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderNotFound(w, r, base)
			return
		}
		s.renderUpstreamError(w, r, base, err)
		return
	}

	sources, err := s.ideas.IdeaSources(r.Context(), id)
	if err != nil {
		// Kart okunduktan sonra kaynak ucu 404 diyorsa kart bu arada
		// kaldırılmıştır: kullanıcıya 404 gösterilir, 502 değil.
		if errors.Is(err, store.ErrNotFound) {
			s.renderNotFound(w, r, base)
			return
		}
		s.renderUpstreamError(w, r, base, err)
		return
	}

	// Sohbet geçmişi kartın yan bilgisidir: uç hata verse bile kart
	// gösterilir, panelde tasarlanmış bir uyarı basılır.
	var msgs []ChatMessage
	chatErr := chatErrorText(base, r.URL.Query().Get("chat_error"))
	if list, err := s.ideas.ListChat(r.Context(), id); err != nil {
		log.Printf("hata: sohbet geçmişi okunamadı (kart %d): %v", id, err)
		if chatErr == "" {
			chatErr = base.T("chat.error.history")
		}
	} else {
		msgs = list
	}

	page := buildIdea(base, idea, sources, msgs, chatErr)
	page.Title = idea.Title + " — " + page.T("app.name")
	page.MobileTitle = idea.Title
	page.Breadcrumb = clipTitle(idea.Title)
	page.ShowBack = true
	page.CopilotActive = true
	s.render(w, r, "idea", http.StatusOK, page)
}

// chatErrorCodes, yönlendirmeyle taşınan hata kodları -> katalog anahtarı.
// Beyaz liste dışındaki değer sessizce yok sayılır (URL'den metin enjekte
// edilemez).
var chatErrorCodes = map[string]string{
	"rate_limited":    "chat.error.rate_limited",
	"upstream":        "chat.error.upstream",
	"no_conversation": "chat.error.no_conversation",
	"empty":           "chat.error.empty",
	"too_long":        "chat.error.too_long",
	"failed":          "chat.error.failed",
	"forbidden":       "chat.error.forbidden",
}

func chatErrorText(base Page, code string) string {
	key, ok := chatErrorCodes[code]
	if !ok {
		return ""
	}
	return base.T(key)
}

// chatErrorCode, apiclient hatasını yönlendirmede taşınacak koda çevirir.
func chatErrorCode(err error) (code string, status int) {
	switch {
	case errors.Is(err, ErrRateLimited):
		return "rate_limited", http.StatusTooManyRequests
	case errors.Is(err, ErrNoConversation):
		return "no_conversation", http.StatusConflict
	case errors.Is(err, ErrUpstream):
		return "upstream", http.StatusBadGateway
	case errors.Is(err, ErrBadRequest):
		return "empty", http.StatusBadRequest
	default:
		return "failed", http.StatusBadGateway
	}
}

// handleChat, `POST /ideas/{id}/chat`. İki yol da aynı doğrulamadan geçer:
//   - form gönderimi (JS yok): 303 ile kart detayına, sohbet çapasına döner;
//   - Accept: application/json (app.js): {"reply","suggestions"} JSON'u.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	base := s.newPage(w, r)
	asJSON := wantsJSON(r)

	id, ok := s.postIdea(w, r, base, asJSON)
	if !ok {
		return
	}

	if err := parseAnyForm(r); err != nil {
		s.chatFailure(w, r, base, id, asJSON, ErrBadRequest)
		return
	}

	msg := firstNonEmpty(r.Form["message"])
	switch {
	case msg == "":
		s.chatRedirect(w, r, base, id, asJSON, "empty", http.StatusBadRequest)
		return
	case len([]rune(msg)) > chatMaxLen:
		s.chatRedirect(w, r, base, id, asJSON, "too_long", http.StatusBadRequest)
		return
	}

	reply, err := s.ideas.SendChat(r.Context(), id, msg, base.Lang)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.chatNotFound(w, r, base, asJSON)
			return
		}
		log.Printf("hata: sohbet gönderilemedi (kart %d): %v", id, err)
		s.chatFailure(w, r, base, id, asJSON, err)
		return
	}

	if asJSON {
		writeJSON(w, http.StatusOK, map[string]any{
			"reply": map[string]any{
				"role":       reply.Reply.Role,
				"message":    reply.Reply.Message,
				"created_at": reply.Reply.CreatedAt,
			},
			"suggestions": suggestionList(reply.Suggestions),
		})
		return
	}
	http.Redirect(w, r, ideaChatURL(id, ""), http.StatusSeeOther)
}

// handleBlend, `POST /ideas/{id}/blend` — sohbetten yeni `ai_blended` kart
// türetir ve yeni kartın detayına yönlendirir.
func (s *Server) handleBlend(w http.ResponseWriter, r *http.Request) {
	base := s.newPage(w, r)
	asJSON := wantsJSON(r)

	id, ok := s.postIdea(w, r, base, asJSON)
	if !ok {
		return
	}

	idea, err := s.ideas.Blend(r.Context(), id, base.Lang)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.chatNotFound(w, r, base, asJSON)
			return
		}
		log.Printf("hata: kart türetilemedi (kart %d): %v", id, err)
		s.chatFailure(w, r, base, id, asJSON, err)
		return
	}

	target := fmt.Sprintf("/ideas/%d", idea.ID)
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]any{"href": target})
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// postIdea, POST uçlarının ortak girişi: CSRF kontrolü + id ayrıştırma.
func (s *Server) postIdea(w http.ResponseWriter, r *http.Request, base Page, asJSON bool) (int64, bool) {
	if !sameOrigin(r) {
		log.Printf("uyarı: çapraz köken POST reddedildi: %s", r.URL.Path)
		if asJSON {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return 0, false
		}
		s.renderForbidden(w, r, base)
		return 0, false
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.chatNotFound(w, r, base, asJSON)
		return 0, false
	}
	return id, true
}

// chatNotFound, POST yolunda kart yoksa: JSON'da 404 gövdesi, form yolunda
// tasarlanmış 404 sayfası.
func (s *Server) chatNotFound(w http.ResponseWriter, r *http.Request, base Page, asJSON bool) {
	if asJSON {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	s.renderNotFound(w, r, base)
}

// chatFailure, hatayı koda çevirip iki yolda da kullanıcıya bildirir.
func (s *Server) chatFailure(w http.ResponseWriter, r *http.Request, base Page, id int64, asJSON bool, err error) {
	code, status := chatErrorCode(err)
	s.chatRedirect(w, r, base, id, asJSON, code, status)
}

// chatRedirect, form yolunda hata kodunu sorgu dizesinde taşıyarak karta
// döner (303 → GET, yenilemede yeniden gönderim olmaz); JSON yolunda kodu
// gövdede ve HTTP durumunda verir.
func (s *Server) chatRedirect(w http.ResponseWriter, r *http.Request, base Page, id int64, asJSON bool, code string, status int) {
	if asJSON {
		writeJSON(w, status, map[string]any{
			"error":   code,
			"message": chatErrorText(base, code),
		})
		return
	}
	http.Redirect(w, r, ideaChatURL(id, code), http.StatusSeeOther)
}

// ideaChatURL, kart detayının sohbet çapasına giden adres.
func ideaChatURL(id int64, errCode string) string {
	u := fmt.Sprintf("/ideas/%d", id)
	if errCode != "" {
		u += "?chat_error=" + url.QueryEscape(errCode)
	}
	return u + "#chat"
}

// renderForbidden, CSRF kontrolünden geçemeyen istek için 403 sayfası.
func (s *Server) renderForbidden(w http.ResponseWriter, r *http.Request, base Page) {
	base.Title = base.T("error.403.title") + " — " + base.T("app.name")
	base.MobileTitle = base.T("app.name")
	s.render(w, r, "error", http.StatusForbidden, ErrorPage{
		Page:    base,
		Status:  http.StatusForbidden,
		Heading: base.T("error.403.title"),
		Message: base.T("error.403.body"),
	})
}

// sameOrigin, basit CSRF kontrolü: Origin (yoksa Referer) başlığının konağı
// isteğin konağıyla aynı olmalı. İkisi de yoksa istek reddedilir — tarayıcı
// form/fetch POST'ları her zaman Origin taşır.
func sameOrigin(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, host)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, host)
	}
	return false
}

// maxFormBytes, multipart gövde için bellek tavanı (mesaj alanı küçüktür).
const maxFormBytes = 1 << 20

// parseAnyForm, hem urlencoded hem multipart gövdeyi okur: app.js urlencoded
// gönderir, elle yazılmış bir istemci multipart gönderirse de mesaj düşmez.
func parseAnyForm(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "multipart/form-data") {
		return r.ParseMultipartForm(maxFormBytes)
	}
	return r.ParseForm()
}

// wantsJSON, app.js'in fetch isteğini form gönderiminden ayırır.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

// firstNonEmpty, aynı adı taşıyan alanlardan (metin girişi + hızlı komut
// çipi) ilk dolu olanı seçer.
func firstNonEmpty(values []string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// suggestionList, öneri dilimini en fazla 3 öğeye indirger ve nil yerine
// boş dilim döner (JSON'da her zaman dizi).
func suggestionList(in []string) []string {
	out := make([]string, 0, 3)
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

// writeJSON, app.js'in beklediği yanıtı yazar.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// handleNotFound, tanımlı olmayan yollar (ve GET dışı metodlar).
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderNotFound(w, r, s.newPage(w, r))
}

func (s *Server) renderNotFound(w http.ResponseWriter, r *http.Request, base Page) {
	base.Title = base.T("error.404.title") + " — " + base.T("app.name")
	base.MobileTitle = base.T("app.name")
	s.render(w, r, "error", http.StatusNotFound, ErrorPage{
		Page:    base,
		Status:  http.StatusNotFound,
		Heading: base.T("error.404.title"),
		Message: base.T("error.404.body"),
	})
}

// renderUpstreamError, API sürecine ulaşılamadığında (ağ hatası, 5xx, bozuk
// yanıt) gösterilen 502 sayfası. Web süreci ayakta kalır; kullanıcı geçici
// bir kesinti olduğunu okur. Hata detayı yalnız log'a düşer, sayfaya değil.
func (s *Server) renderUpstreamError(w http.ResponseWriter, r *http.Request, base Page, err error) {
	log.Printf("hata: api yanıt vermedi: %s %s: %v", r.Method, r.URL.Path, err)
	base.Title = base.T("error.502.title") + " — " + base.T("app.name")
	base.MobileTitle = base.T("app.name")
	s.render(w, r, "error", http.StatusBadGateway, ErrorPage{
		Page:    base,
		Status:  http.StatusBadGateway,
		Heading: base.T("error.502.title"),
		Message: base.T("error.502.body"),
	})
}

func (s *Server) renderServerError(w http.ResponseWriter, r *http.Request, base Page, err error) {
	log.Printf("hata: %s %s: %v", r.Method, r.URL.Path, err)
	base.Title = base.T("error.500.title") + " — " + base.T("app.name")
	base.MobileTitle = base.T("app.name")
	s.render(w, r, "error", http.StatusInternalServerError, ErrorPage{
		Page:    base,
		Status:  http.StatusInternalServerError,
		Heading: base.T("error.500.title"),
		Message: base.T("error.500.body"),
	})
}

// render, şablonu ÖNCE tampona yazar: render ortasında hata çıkarsa yarım
// HTML + yanlış durum kodu gönderilmez.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, data any) {
	tpl, ok := s.tpl[name]
	if !ok {
		log.Printf("hata: bilinmeyen şablon %q", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		log.Printf("hata: şablon %q render: %v", name, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Dil/tema tercihi yanıtı değiştirir; ara cache'ler karıştırmasın.
	w.Header().Add("Vary", "Cookie")
	w.Header().Add("Vary", "Accept-Language")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// newPage, istekten dil/tema tercihini çözer, gerekiyorsa cookie yazar ve
// JS'siz çalışan dil/tema bağlantılarını hazırlar.
func (s *Server) newPage(w http.ResponseWriter, r *http.Request) Page {
	lang, persistLang := resolveLang(r)
	if persistLang {
		setPrefCookie(w, r, cookieLang, lang)
	}

	theme, persistTheme := resolveTheme(r)
	if persistTheme {
		setPrefCookie(w, r, cookieTheme, theme)
	}

	next := "dark"
	if theme == "dark" {
		next = "light"
	}

	return Page{
		Lang:       lang,
		Theme:      theme,
		AssetVer:   s.assetVer,
		LangLinkTR: linkWith(r.URL, "lang", "tr"),
		LangLinkEN: linkWith(r.URL, "lang", "en"),
		ThemeLink:  linkWith(r.URL, "theme", next),
		NextTheme:  next,
	}
}

// resolveTheme, tema tercihini çözer: ?theme= > cookie > boş (boş = CSS
// prefers-color-scheme'e bırakılır). İkinci değer cookie yazılmalı mı.
func resolveTheme(r *http.Request) (string, bool) {
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("theme"))); q == "light" || q == "dark" {
		return q, true
	}
	if c, err := r.Cookie(cookieTheme); err == nil && (c.Value == "light" || c.Value == "dark") {
		return c.Value, false
	}
	return "", false
}

// setPrefCookie, tercih cookie'sini yazar. HttpOnly DEĞİLDİR: app.js aynı
// tercihi yazabilsin diye. Secure yalnız istek TLS üzerindeyse (Railway'de
// X-Forwarded-Proto ile) set edilir ki lokal http'de de çalışsın.
func setPrefCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: false,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// linkWith, mevcut URL'nin sorgu dizesine tek parametre ekleyip/değiştirip
// göreli bağlantı üretir — filtre ve arama korunur, JS gerekmez.
func linkWith(u *url.URL, key, value string) string {
	v := url.Values{}
	if u != nil {
		v = cloneValues(u.Query())
	}
	v.Set(key, value)
	path := "/"
	if u != nil && u.Path != "" {
		path = u.Path
	}
	return path + "?" + v.Encode()
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, vs := range in {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
