// Package api, IdeaLode'un JSON API sunucusunu (idealode api, #18) sağlar.
// DATABASE_URL'i gören TEK süreç budur; `serve` (web) buraya HTTP ile
// bağlanır (bkz. docs/specs/faz2-dilim1b-api.md — sözleşme).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// apiTimeout, her isteğin üst sınırı (sözleşme: tüm yanıtlar JSON —
// stdlib'in http.TimeoutHandler'ı zaman aşımında düz metin gövde yazdığı
// için burada elle uygulanır, bkz. timeoutMiddleware).
const apiTimeout = 5 * time.Second

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
	ideas   IdeaStore
	mux     *http.ServeMux
	timeout time.Duration
}

// NewServer, handler'ları kurar.
func NewServer(ideas IdeaStore) *Server {
	return newServer(ideas, apiTimeout)
}

// newServer, testlerin gerçek bir sunucuda (httptest.NewServer) kısa
// zaman aşımıyla deneyebilmesi için NewServer'ın iç uygulaması.
func newServer(ideas IdeaStore, timeout time.Duration) *Server {
	s := &Server{ideas: ideas, timeout: timeout}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/ideas", s.handleListIdeas)
	mux.HandleFunc("GET /api/ideas/{id}", s.handleGetIdea)
	mux.HandleFunc("GET /api/ideas/{id}/sources", s.handleIdeaSources)
	mux.HandleFunc("/", s.handleNotFound) // eşleşmeyen her yol
	s.mux = mux
	return s
}

// Handler, log + zaman aşımı + recover sarmalıyla mux'ı döner. recoverPanic
// en içte durur ki panic, timeoutMiddleware'in başlattığı handler
// goroutine'i içinde (aynı goroutine yığınında) yakalansın.
func (s *Server) Handler() http.Handler {
	return requestLog(s.timeoutMiddleware(s.recoverPanic(s.mux)))
}

// timeoutMiddleware, isteği s.timeout ile sınırlar. stdlib'in
// http.TimeoutHandler'ı zaman aşımında düz metin/HTML gövde yazar — bu
// sözleşmeyi ("tüm yanıtlar JSON") bozar. Burada handler ayrı bir
// goroutine'de çalıştırılır; süre dolarsa ve handler henüz yanıt
// yazmadıysa sözleşmedeki JSON hata gövdesiyle 503 dönülür. Handler
// context iptalini görüp döndükten sonra kendi yazdıklarıysa timeoutWriter
// tarafından sessizce yutulur (istemciye zaten 503 gitmiştir).
func (s *Server) timeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
		defer cancel()

		tw := newTimeoutWriter(w)
		done := make(chan struct{})
		go func() {
			defer close(done)
			next.ServeHTTP(tw, r.WithContext(ctx))
		}()

		select {
		case <-done:
			tw.flush()
		case <-ctx.Done():
			tw.timeout()
		}
	})
}

// timeoutWriter, handler ile zaman aşımı yolunun aynı gerçek
// http.ResponseWriter'a yarışmadan yazmasını sağlar. Handler goroutine'i
// (Header/Write/WriteHeader) hiçbir zaman gerçek writer'a dokunmaz — yalnız
// kendi özel header/buffer'ına yazar, bunlar tw.mu ile korunur. Gerçek
// writer'a tek bir yazar dokunur: ya timeoutMiddleware'in ana goroutine'i
// handler bitince flush() ile (done kanalının kapanması happens-before
// garantisi verir, ek kilit gerekmez), ya da süre dolunca timeout() ile —
// select bu ikisini birbirini dışlar kılar, aynı anda ikisi de çalışmaz.
type timeoutWriter struct {
	http.ResponseWriter
	mu       sync.Mutex
	header   http.Header // handler'ın Header() ile gördüğü özel kopya
	buf      bytes.Buffer
	status   int
	wrote    bool // handler en az bir WriteHeader/Write yaptı mı
	timedOut bool
}

func newTimeoutWriter(w http.ResponseWriter) *timeoutWriter {
	return &timeoutWriter{ResponseWriter: w, header: make(http.Header)}
}

// Header, yalnız handler goroutine'i tarafından çağrılır (zaman aşımı yolu
// kendi ayrı hata yanıtını doğrudan gerçek writer'a yazar, bu map'e hiç
// dokunmaz) — kilit gerekmez.
func (tw *timeoutWriter) Header() http.Header {
	return tw.header
}

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut || tw.wrote {
		return
	}
	tw.wrote = true
	tw.status = code
}

func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return len(b), nil // istemciye zaten 503 gitti; handler'ın gövdesi yutulur
	}
	if !tw.wrote {
		tw.wrote = true
		tw.status = http.StatusOK
	}
	return tw.buf.Write(b)
}

// flush, handler süre dolmadan bitince (done kanalı kapanınca) biriktirilen
// header/gövdeyi gerçek writer'a aktarır.
func (tw *timeoutWriter) flush() {
	tw.mu.Lock()
	timedOut := tw.timedOut
	status := tw.status
	tw.mu.Unlock()
	if timedOut {
		return // yarış: timeout() zaten kazandı (pratikte olmaz, savunma amaçlı)
	}
	if status == 0 {
		status = http.StatusOK
	}
	dst := tw.ResponseWriter.Header()
	for k, v := range tw.header {
		dst[k] = v
	}
	tw.ResponseWriter.WriteHeader(status)
	tw.ResponseWriter.Write(tw.buf.Bytes())
}

// timeout, süre dolduğunda çağrılır. Handler'ın biriktirdiği gövdeyi (varsa)
// atar ve gerçek writer'a doğrudan 503 JSON yazar.
func (tw *timeoutWriter) timeout() {
	tw.mu.Lock()
	if tw.timedOut {
		tw.mu.Unlock()
		return
	}
	tw.timedOut = true
	tw.mu.Unlock()
	writeError(tw.ResponseWriter, http.StatusServiceUnavailable, "timeout")
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
