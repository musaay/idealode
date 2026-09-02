// Package web, IdeaLode'un salt okunur web arayüzünü (galeri + kart detayı)
// sunar. Sunucuda render edilir: Go html/template + embed.FS; React/Node
// toolchain yoktur. Şablonlar, i18n katalogları ve statik dosyalar binary'ye
// gömülüdür ve süreç başlangıcında bir kez hazırlanır.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Cookie adları — tercihler sunucuda okunur, JS zorunlu değildir.
const (
	cookieLang  = "lang"
	cookieTheme = "theme"
)

// cookieMaxAge, tercih cookie'lerinin ömrü (1 yıl).
const cookieMaxAge = 365 * 24 * 60 * 60

// contentSecurityPolicy, spec'te tanımlı sabit politika. Inline script yok;
// dış kaynaklardan yalnız Google Fonts stili/fontu yüklenir.
const contentSecurityPolicy = "default-src 'self'; style-src 'self' fonts.googleapis.com; font-src fonts.gstatic.com"

// IdeaStore, web katmanının store'dan ihtiyaç duyduğu okuma yüzeyi.
// Somut *store.Store yerine arayüz kullanılır ki handler testleri canlı
// veritabanı olmadan fake ile koşsun.
type IdeaStore interface {
	ListIdeasFiltered(ctx context.Context, f store.IdeaFilter) ([]store.Idea, error)
	GetIdea(ctx context.Context, id int64) (*store.Idea, error)
	IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error)
}

// Server, HTTP handler'larını ve önceden parse edilmiş şablonları taşır.
type Server struct {
	ideas    IdeaStore
	tpl      map[string]*template.Template
	assetVer string
	static   http.Handler
	mux      *http.ServeMux
}

// pageTemplates, sayfa adı -> şablon dosyası. Her sayfa layout ile birlikte
// ayrı bir şablon kümesine parse edilir ("content" bloğu çakışmasın).
var pageTemplates = map[string]string{
	"gallery": "templates/gallery.html",
	"idea":    "templates/idea.html",
	"error":   "templates/error.html",
}

// NewServer, handler'ları kurar. Şablon hatası burada panic'e döner
// (template.Must semantiği): bozuk şablonla ayağa kalkmak yerine erken çök.
func NewServer(ideas IdeaStore) *Server {
	s := &Server{
		ideas:    ideas,
		tpl:      mustParseTemplates(),
		assetVer: mustAssetVersion(),
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("static alt dizini: %v", err))
	}
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /static/", s.handleStatic)
	mux.HandleFunc("GET /ideas/{id}", s.handleIdea)
	mux.HandleFunc("GET /{$}", s.handleGallery)
	mux.HandleFunc("/", s.handleNotFound) // eşleşmeyen her yol
	s.mux = mux
	return s
}

// mustParseTemplates, tüm sayfaları başlangıçta bir kez parse eder.
func mustParseTemplates() map[string]*template.Template {
	out := make(map[string]*template.Template, len(pageTemplates))
	for name, file := range pageTemplates {
		out[name] = template.Must(
			template.New("layout.html").ParseFS(templateFS, "templates/layout.html", file))
	}
	return out
}

// mustAssetVersion, gömülü statik dosyaların içeriğinden kısa bir sürüm
// damgası üretir; app.css/app.js bağlantıları `?v=` ile bunu taşır, böylece
// uzun cache güvenle kullanılabilir ve deploy'da tarayıcı yeniyi çeker.
func mustAssetVersion() string {
	h := sha256.New()
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("statik sürüm damgası: %v", err))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Handler, güvenlik başlıkları + log + recover sarmalıyla mux'ı döner.
func (s *Server) Handler() http.Handler {
	return securityHeaders(requestLog(s.recoverPanic(s.mux)))
}

// ListenAndServe, sunucuyu addr üzerinde çalıştırır ve ctx iptal edilince
// zarifçe kapatır.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("serve: %s dinleniyor", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Printf("serve: kapanıyor")
		return srv.Shutdown(shutdownCtx)
	}
}

// securityHeaders, her yanıta sabit güvenlik başlıklarını ekler.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// statusRecorder, log için yanıt kodunu yakalar.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// requestLog, istek satırını süresiyle loglar. Sorgu dizesi loglanmaz —
// arama metni kullanıcı verisidir, log'a düşmez.
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

// recoverPanic, handler panic'ini tasarlanmış 500 sayfasına çevirir; süreç
// ölmez. Yanıt henüz başlamadıysa şablon render edilir (render tampona yazar,
// dolayısıyla panic anına kadar gövdeye bir şey gitmemiştir).
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v (%s %s)", rec, r.Method, r.URL.Path)
				s.renderServerError(w, r, s.newPage(w, r), fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
