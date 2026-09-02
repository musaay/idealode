// Package api, IdeaLode'un JSON API sunucusunu (idealode api, #18) sağlar.
// DATABASE_URL'i gören TEK süreç budur; `serve` (web) buraya HTTP ile
// bağlanır (bkz. docs/specs/faz2-dilim1b-api.md — sözleşme).
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// IdeaStore, api katmanının store'dan ihtiyaç duyduğu okuma yüzeyi. Somut
// *store.Store yerine arayüz kullanılır ki handler testleri canlı
// veritabanı olmadan fake ile koşsun (web.IdeaStore ile aynı desen — api
// paketi kendi eşdeğerini tanımlar, web paketine bağımlı değildir).
type IdeaStore interface {
	ListIdeasFiltered(ctx context.Context, f store.IdeaFilter) ([]store.Idea, error)
	GetIdea(ctx context.Context, id int64) (*store.Idea, error)
	IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error)
}

// Server, JSON API handler'larını taşır.
type Server struct {
	ideas IdeaStore
	mux   *http.ServeMux
}

// NewServer, handler'ları kurar.
func NewServer(ideas IdeaStore) *Server {
	s := &Server{ideas: ideas}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/ideas", s.handleListIdeas)
	mux.HandleFunc("GET /api/ideas/{id}", s.handleGetIdea)
	mux.HandleFunc("GET /api/ideas/{id}/sources", s.handleIdeaSources)
	mux.HandleFunc("/", s.handleNotFound) // eşleşmeyen her yol
	s.mux = mux
	return s
}

// Handler, log + recover + zaman aşımı sarmalıyla mux'ı döner.
func (s *Server) Handler() http.Handler {
	return requestLog(s.recoverPanic(http.TimeoutHandler(s.mux, 5*time.Second, `{"error":"internal"}`)))
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
		log.Printf("api: %s dinleniyor", addr)
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
		log.Printf("api: kapanıyor")
		return srv.Shutdown(shutdownCtx)
	}
}

// statusRecorder, log için yanıt kodunu yakalar.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
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

// recoverPanic, handler panic'ini 500 JSON yanıtına çevirir; süreç ölmez.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v (%s %s)", rec, r.Method, r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON, gövdeyi sözleşmedeki Content-Type ile yazar.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("hata: JSON yazımı: %v", err)
	}
}

// writeError, sözleşmedeki hata gövdesini yazar: {"error":"<code>"}.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
