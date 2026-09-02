package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	if idea.ParentIdeaID != nil || idea.Mine {
		t.Errorf("ek alanlar boş beklenirdi: %+v", idea)
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

// --------------------------------------------------- Idea Copilot (dilim 2)

func TestGetIdeaCarriesParentAndMine(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusOK,
		`{"idea":{"id":9,"title":"Türetilmiş","source_type":"ai_blended","parent_idea_id":3,"mine":true}}`))

	idea, err := c.GetIdea(context.Background(), 9)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if idea.ID != 9 || idea.SourceType != "ai_blended" {
		t.Errorf("kart yanlış: %+v", idea)
	}
	if idea.ParentIdeaID == nil || *idea.ParentIdeaID != 3 || !idea.Mine {
		t.Errorf("ek alanlar = %+v", idea)
	}
}

func TestSessionHeaderSentFromContext(t *testing.T) {
	var gotSID, gotMethod, gotPath string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotSID = r.Header.Get("X-Session-Id")
		gotMethod, gotPath = r.Method, r.URL.Path
		jsonHandler(http.StatusOK, `{"messages":[]}`)(w, r)
	})

	ctx := web.WithSession(context.Background(), "abc123")
	if _, err := c.ListChat(ctx, 5); err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotSID != "abc123" {
		t.Errorf("X-Session-Id = %q", gotSID)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/ideas/5/chat" {
		t.Errorf("istek = %s %s", gotMethod, gotPath)
	}
}

func TestListChatHappyPathAndNeverNil(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusOK,
		`{"messages":[{"id":"m1","role":"user","message":"Selam","created_at":"2026-09-02T10:00:00Z"},`+
			`{"id":"m2","role":"assistant","message":"Merhaba","created_at":"2026-09-02T10:00:05Z"}]}`))

	msgs, err := c.ListChat(context.Background(), 1)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Message != "Merhaba" {
		t.Fatalf("mesajlar = %+v", msgs)
	}
	if msgs[0].CreatedAt.UTC().Format(time.RFC3339) != "2026-09-02T10:00:00Z" {
		t.Errorf("zaman damgası = %v", msgs[0].CreatedAt)
	}

	empty := newFake(t, jsonHandler(http.StatusOK, `{"messages":null}`))
	got, err := empty.ListChat(context.Background(), 1)
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("boş geçmiş nil dönmemeli: %#v", got)
	}
}

func TestSendChatSendsBodyAndParsesReply(t *testing.T) {
	var gotBody, gotType string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType = string(b), r.Header.Get("Content-Type")
		jsonHandler(http.StatusOK,
			`{"reply":{"id":"m9","role":"assistant","message":"Cevap","created_at":"2026-09-02T11:00:00Z"},`+
				`"suggestions":["a","b"]}`)(w, r)
	})

	reply, err := c.SendChat(context.Background(), 4, "Soru", "tr")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if !strings.Contains(gotBody, `"message":"Soru"`) || !strings.Contains(gotBody, `"lang":"tr"`) {
		t.Errorf("gövde = %s", gotBody)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if reply.Reply.Message != "Cevap" || len(reply.Suggestions) != 2 {
		t.Errorf("cevap = %+v", reply)
	}
}

func TestChatStatusCodesMapToTypedErrors(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, web.ErrBadRequest},
		{http.StatusNotFound, store.ErrNotFound},
		{http.StatusConflict, web.ErrNoConversation},
		{http.StatusTooManyRequests, web.ErrRateLimited},
		{http.StatusBadGateway, web.ErrUpstream},
	}
	for _, tt := range tests {
		c := newFake(t, jsonHandler(tt.status, `{"error":"x"}`))
		_, err := c.SendChat(context.Background(), 1, "soru", "tr")
		if !errors.Is(err, tt.want) {
			t.Errorf("durum %d -> hata %v, beklenen %v", tt.status, err, tt.want)
		}
	}

	// Beklenmeyen durum tipli hataya çevrilmez (web 502 sayfası gösterir).
	c := newFake(t, jsonHandler(http.StatusTeapot, `{}`))
	_, err := c.SendChat(context.Background(), 1, "soru", "tr")
	if err == nil || errors.Is(err, web.ErrUpstream) {
		t.Errorf("beklenmeyen durum yanlış eşlendi: %v", err)
	}
}

func TestBlendParsesCreatedIdea(t *testing.T) {
	var gotPath, gotMethod string
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		jsonHandler(http.StatusCreated,
			`{"idea":{"id":42,"title":"Yeni","source_type":"ai_blended","evidence_count":4}}`)(w, r)
	})

	idea, err := c.Blend(context.Background(), 7, "en")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/ideas/7/blend" {
		t.Errorf("istek = %s %s", gotMethod, gotPath)
	}
	if idea.ID != 42 || idea.SourceType != "ai_blended" {
		t.Errorf("kart = %+v", idea)
	}
}

func TestBlendMissingIdeaIsError(t *testing.T) {
	c := newFake(t, jsonHandler(http.StatusCreated, `{}`))
	if _, err := c.Blend(context.Background(), 7, "tr"); err == nil {
		t.Fatal("sözleşme ihlali hata dönmeliydi")
	}
}
