package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/store"
	"github.com/musaay/idealode/api/internal/web"
)

// Client, web katmanının beklediği arayüzü uygulamalı — derleme zamanı kontrolü.
var _ web.IdeaStore = (*Client)(nil)

// newFake, verilen handler'ı çalıştıran sahte API + ona bağlı istemci döner.
// Canlı ağ yoktur; httptest yerel dinleyici kullanır.
func newFake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, 2*time.Second)
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestListIdeasHappyPath(t *testing.T) {
	var gotPath, gotQuery string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		jsonHandler(http.StatusOK, `{"ideas":[
			{"id":1,"title":"Randevu botu","problem_statement":"P","proposed_solution":"S",
			 "target_user":"KOBİ","evidence_count":4,"example_quotes":["birebir alıntı"],
			 "source_type":"pain_point","domain_tags":["smb"],"local_evidence":[],
			 "urgency_score":4,"monetization_signal":3,"created_at":"2026-08-21T12:00:00Z"}]}`)(w, r)
	})

	ideas, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{
		SourceType: "pain_point", Query: "randevu", Limit: 25,
	})
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotPath != "/api/ideas" {
		t.Errorf("yol = %q, /api/ideas bekleniyor", gotPath)
	}
	if gotQuery != "limit=25&q=randevu&source_type=pain_point" {
		t.Errorf("sorgu = %q", gotQuery)
	}
	if len(ideas) != 1 {
		t.Fatalf("kart sayısı = %d, 1 bekleniyor", len(ideas))
	}
	i := ideas[0]
	if i.ID != 1 || i.Title != "Randevu botu" || i.EvidenceCount != 4 {
		t.Errorf("kart alanları yanlış çözüldü: %+v", i)
	}
	if len(i.ExampleQuotes) != 1 || i.ExampleQuotes[0] != "birebir alıntı" {
		t.Errorf("alıntı birebir değil: %v", i.ExampleQuotes)
	}
	if !i.CreatedAt.Equal(time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at = %v", i.CreatedAt)
	}
}

func TestListIdeasNoFilterSendsNoQuery(t *testing.T) {
	var gotQuery string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		jsonHandler(http.StatusOK, `{"ideas":[]}`)(w, r)
	})
	if _, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{}); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("boş filtrede sorgu dizesi = %q, boş bekleniyor", gotQuery)
	}
}

func TestListIdeasEmptyAndNilNeverNil(t *testing.T) {
	for name, body := range map[string]string{
		"boş dizi": `{"ideas":[]}`,
		"null":     `{"ideas":null}`,
		"alan yok": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newFake(t, jsonHandler(http.StatusOK, body))
			ideas, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{})
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if ideas == nil {
				t.Fatal("nil dilim döndü, boş dilim bekleniyor")
			}
			if len(ideas) != 0 {
				t.Errorf("uzunluk = %d, 0 bekleniyor", len(ideas))
			}
		})
	}
}

func TestGetIdeaHappyPath(t *testing.T) {
	var gotPath string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonHandler(http.StatusOK, `{"idea":{"id":7,"title":"Kart","source_type":"market_derived"}}`)(w, r)
	})
	idea, err := c.GetIdea(context.Background(), 7)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotPath != "/api/ideas/7" {
		t.Errorf("yol = %q", gotPath)
	}
	if idea.ID != 7 || idea.Title != "Kart" {
		t.Errorf("kart yanlış: %+v", idea)
	}
}

func TestGetIdeaNotFound(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusNotFound, `{"error":"not_found"}`))
	_, err := c.GetIdea(context.Background(), 404)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hata = %v, store.ErrNotFound bekleniyor", err)
	}
}

func TestGetIdeaMissingIdeaFieldIsError(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusOK, `{}`))
	_, err := c.GetIdea(context.Background(), 1)
	if err == nil {
		t.Fatal("hata bekleniyordu")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("sözleşme ihlali ErrNotFound'a çevrilmemeli")
	}
}

func TestIdeaSourcesHappyPathAndZeroTime(t *testing.T) {
	var gotPath string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonHandler(http.StatusOK, `{"sources":[
			{"platform":"hackernews","community":"news","url":"https://example.com/a","created_at":"2026-08-20T09:30:00Z"},
			{"platform":"radar_seed","community":"","url":"https://example.com/b"}]}`)(w, r)
	})
	src, err := c.IdeaSources(context.Background(), 3)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotPath != "/api/ideas/3/sources" {
		t.Errorf("yol = %q", gotPath)
	}
	if len(src) != 2 {
		t.Fatalf("kaynak sayısı = %d, 2 bekleniyor", len(src))
	}
	if src[0].Platform != "hackernews" || src[0].URL != "https://example.com/a" {
		t.Errorf("ilk kaynak yanlış: %+v", src[0])
	}
	if src[0].CreatedAt.IsZero() {
		t.Error("created_at çözülemedi")
	}
	if !src[1].CreatedAt.IsZero() {
		t.Errorf("created_at atlanmışsa sıfır zaman bekleniyor: %v", src[1].CreatedAt)
	}
}

func TestIdeaSourcesEmptyNeverNil(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusOK, `{"sources":[]}`))
	src, err := c.IdeaSources(context.Background(), 1)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if src == nil {
		t.Fatal("nil dilim döndü, boş dilim bekleniyor")
	}
}

func TestIdeaSourcesNotFound(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusNotFound, `{"error":"not_found"}`))
	_, err := c.IdeaSources(context.Background(), 9)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hata = %v, store.ErrNotFound bekleniyor", err)
	}
}

func TestServerErrorIsWrapped(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		c := newFake(t, jsonHandler(status, `{"error":"internal"}`))
		_, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{})
		if err == nil {
			t.Fatalf("%d için hata bekleniyordu", status)
		}
		if errors.Is(err, store.ErrNotFound) {
			t.Errorf("%d ErrNotFound'a çevrilmemeli", status)
		}
	}
}

func TestMalformedJSONIsWrapped(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusOK, `{"ideas":[{"id":`))
	_, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{})
	if err == nil {
		t.Fatal("bozuk JSON için hata bekleniyordu")
	}
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		t.Errorf("altta yatan JSON hatası sarılmamış: %v", err)
	}
}

func TestNetworkFailureIsWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // dinleyici kapalı: bağlantı reddedilir

	c := New(base, 2*time.Second)
	_, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{})
	if err == nil {
		t.Fatal("ağ hatası bekleniyordu")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("ağ hatası ErrNotFound'a çevrilmemeli")
	}
}

func TestTimeoutIsWrapped(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	c := New(srv.URL, 50*time.Millisecond)
	_, err := c.GetIdea(context.Background(), 1)
	if err == nil {
		t.Fatal("zaman aşımı hatası bekleniyordu")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !os.IsTimeout(err) {
		t.Errorf("zaman aşımı hatası korunmadı: %v", err)
	}
}

func TestContextCancellationRespected(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	c := New(srv.URL, 5*time.Second)
	_, err := c.ListIdeasFiltered(ctx, store.IdeaFilter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("hata = %v, context.Canceled bekleniyor", err)
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonHandler(http.StatusOK, `{"ideas":[]}`)(w, r)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL+"///", 2*time.Second)
	if _, err := c.ListIdeasFiltered(context.Background(), store.IdeaFilter{}); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotPath != "/api/ideas" {
		t.Errorf("yol = %q, /api/ideas bekleniyor", gotPath)
	}
}
