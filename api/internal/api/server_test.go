package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// fakeStore, canlı veritabanı olmadan handler testleri için IdeaStore'u
// uygular.
type fakeStore struct {
	ideas       []store.Idea
	byID        map[int64]*store.Idea
	sources     map[int64][]store.IdeaSource
	lastFilter  store.IdeaFilter
	listErr     error
	sourcesErr  error
	forceGetErr error         // ErrNotFound dışı bir hata simüle etmek için
	delay       time.Duration // gerçek sunucuda zaman aşımını tetiklemek için (bkz. TestTimeout_RealServer)
}

func (f *fakeStore) ListIdeasFiltered(ctx context.Context, filt store.IdeaFilter) ([]store.Idea, error) {
	f.lastFilter = filt
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	if filt.SourceType == "" {
		return f.ideas, nil
	}
	out := []store.Idea{}
	for _, i := range f.ideas {
		if i.SourceType == filt.SourceType {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeStore) GetIdea(ctx context.Context, id int64) (*store.Idea, error) {
	if f.forceGetErr != nil {
		return nil, f.forceGetErr
	}
	idea, ok := f.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return idea, nil
}

func (f *fakeStore) IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error) {
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return f.sources[ideaID], nil // kasıtlı: nil dönebilir (nil-slice testi)
}

func newFakeStore() *fakeStore {
	idea1 := store.Idea{ID: 1, Title: "Kart 1", SourceType: "pain_point"}
	idea2 := store.Idea{ID: 2, Title: "Kart 2", SourceType: "market_derived"}
	return &fakeStore{
		ideas: []store.Idea{idea1, idea2},
		byID:  map[int64]*store.Idea{1: &idea1, 2: &idea2},
		sources: map[int64][]store.IdeaSource{
			1: {{Platform: "reddit", Community: "r/test", URL: "https://x", CreatedAt: time.Now()}},
		},
	}
}

func doReq(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	assertContentType(t, rec)
	if rec.Body.String() != `{"status":"ok"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestListIdeas_Happy(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertContentType(t, rec)

	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Ideas) != 2 {
		t.Fatalf("2 idea beklenirdi, geldi: %d", len(body.Ideas))
	}
}

func TestListIdeas_SourceTypeFilter(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=pain_point")
	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Ideas) != 1 || body.Ideas[0].SourceType != "pain_point" {
		t.Errorf("filtre çalışmadı: %+v", body.Ideas)
	}
}

func TestListIdeas_UnknownSourceType(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=bilinmeyen")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"ideas":[]}`+"\n" {
		t.Errorf("bilinmeyen source_type boş liste dönmeli, geldi: %s", rec.Body.String())
	}
}

func TestListIdeas_EmptyResult_NotNull(t *testing.T) {
	fs := newFakeStore()
	fs.ideas = nil // nil slice tuzağı
	s := NewServer(fs)
	rec := doReq(t, s.Handler(), "/api/ideas?source_type=pain_point")
	if strings.Contains(rec.Body.String(), "null") {
		t.Errorf("nil slice null'a sızmış: %s", rec.Body.String())
	}
	if rec.Body.String() != `{"ideas":[]}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestListIdeas_LimitBounds(t *testing.T) {
	fs := newFakeStore()
	s := NewServer(fs)

	doReq(t, s.Handler(), "/api/ideas?limit=0")
	if fs.lastFilter.Limit != 0 {
		t.Errorf("limit=0 filtreye 0 gitmeli (default kararı store'a bırakılır), geldi: %d", fs.lastFilter.Limit)
	}

	doReq(t, s.Handler(), "/api/ideas?limit=abc")
	if fs.lastFilter.Limit != 0 {
		t.Errorf("geçersiz limit savunmacı 0'a düşmeli, geldi: %d", fs.lastFilter.Limit)
	}

	doReq(t, s.Handler(), "/api/ideas?limit=500")
	if fs.lastFilter.Limit != 500 {
		t.Errorf("ham limit filtreye geçmeli (üst sınır store'da uygulanır), geldi: %d", fs.lastFilter.Limit)
	}
}

func TestGetIdea_Happy(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Idea store.Idea `json:"idea"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Idea.ID != 1 {
		t.Errorf("id: %d", body.Idea.ID)
	}
}

func TestGetIdea_NotFound(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"not_found"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestGetIdea_InvalidID(t *testing.T) {
	s := NewServer(newFakeStore())
	for _, id := range []string{"abc", "-1", "0", "1.5"} {
		rec := doReq(t, s.Handler(), "/api/ideas/"+id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("id=%q: status %d bekleniyordu 404", id, rec.Code)
		}
	}
}

func TestIdeaSources_Happy(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/1/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sources []store.IdeaSource `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("1 kaynak beklenirdi, geldi: %d", len(body.Sources))
	}
}

func TestIdeaSources_EmptyNotNull(t *testing.T) {
	// idea 2 var ama sources map'inde kaydı yok -> fakeStore nil döner.
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/2/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "null") {
		t.Errorf("nil slice null'a sızmış: %s", rec.Body.String())
	}
	if rec.Body.String() != `{"sources":[]}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func TestIdeaSources_CardNotFound(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/999/sources")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestIdeaSources_InvalidID(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/api/ideas/abc/sources")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestNotFoundPath(t *testing.T) {
	s := NewServer(newFakeStore())
	rec := doReq(t, s.Handler(), "/bilinmeyen-yol")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"not_found"}`+"\n" {
		t.Errorf("gövde: %s", rec.Body.String())
	}
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	got := rec.Header().Get("Content-Type")
	if got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: %q", got)
	}
}

// TestTimeout_RealServer, httptest.ResponseRecorder DEĞİL gerçek bir
// sunucu (httptest.NewServer) üzerinden zaman aşımını doğrular: stdlib'in
// http.TimeoutHandler'ı 503 + text/plain döner, sözleşme ise tüm yanıtların
// JSON olmasını ister. Yavaş handler'ı simülemek için fakeStore.delay,
// sunucunun zaman aşımından uzun tutulur.
func TestTimeout_RealServer(t *testing.T) {
	fs := newFakeStore()
	fs.delay = 80 * time.Millisecond
	s := newServer(fs, 10*time.Millisecond)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/ideas")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (503 bekleniyordu)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type: %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("gövde JSON değil: %v", err)
	}
	if body["error"] != "timeout" {
		t.Errorf(`gövde {"error":"timeout"} olmalıydı, geldi: %v`, body)
	}

	// fakeStore'un arkaplan goroutine'i gecikmesini tamamlayıp yazmaya
	// çalıştığında timeoutWriter bunu sessizce yutmalı (ikinci yanıt
	// gitmemeli) — test sürecinin goroutine'i temiz bitirmesine izin ver.
	time.Sleep(fs.delay + 40*time.Millisecond)
}
