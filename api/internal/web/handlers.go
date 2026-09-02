package web

import (
	"bytes"
	"errors"
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
		s.renderServerError(w, r, base, err)
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
		s.renderServerError(w, r, base, err)
		return
	}

	sources, err := s.ideas.IdeaSources(r.Context(), id)
	if err != nil {
		s.renderServerError(w, r, base, err)
		return
	}

	page := buildIdea(base, idea, sources)
	page.Title = idea.Title + " — " + page.T("app.name")
	page.MobileTitle = idea.Title
	page.Breadcrumb = clipTitle(idea.Title)
	page.ShowBack = true
	s.render(w, r, "idea", http.StatusOK, page)
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
