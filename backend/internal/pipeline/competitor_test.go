package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

// fakeSearchChat: arama destekli sahte istemci (canlı LLM YOK).
type fakeSearchChat struct {
	answer      string
	err         error
	searchCalls atomic.Int32
	gotSystem   string
}

func (f *fakeSearchChat) ChatJSON(ctx context.Context, s, u string) (string, error) {
	return "{}", nil
}
func (f *fakeSearchChat) ChatJSONWithTemperature(ctx context.Context, s, u string, t float64) (string, error) {
	return "{}", nil
}
func (f *fakeSearchChat) ChatWithBrowserSearch(ctx context.Context, system, user string) (string, error) {
	f.searchCalls.Add(1)
	f.gotSystem = system
	return f.answer, f.err
}
func (f *fakeSearchChat) ModelName() string { return "fake-search" }

// allowLoopbackHost: testte httptest sunucusuna (127.0.0.1) izin verir, geri
// kalan host'lar sahte DNS ile gerçek denetimden geçer.
func testVerifier(srv *httptest.Server, lookup map[string]string) competitorVerifier {
	return competitorVerifier{
		Client: srv.Client(),
		CheckHost: func(ctx context.Context, host string) error {
			if host == "127.0.0.1" {
				return nil
			}
			return checkPublicHost(ctx, host, func(ctx context.Context, h string) ([]net.IP, error) {
				ip, ok := lookup[h]
				if !ok {
					return nil, errors.New("bulunamadı")
				}
				return []net.IP{net.ParseIP(ip)}, nil
			})
		},
	}
}

func newCompetitorTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<h1>Welcome to KiwiCo</h1>") })
	mux.HandleFunc("/ok2", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "MEL Science kits") })
	mux.HandleFunc("/noname", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tamamen alakasız sayfa") })
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://internal.test/kiwico", http.StatusFound)
	})
	return httptest.NewTLSServer(mux)
}

func TestVerifyCompetitor(t *testing.T) {
	srv := newCompetitorTestServer()
	defer srv.Close()
	v := testVerifier(srv, map[string]string{"internal.test": "10.0.0.5", "evil.test": "192.168.1.9"})
	ctx := context.Background()

	cases := []struct {
		name string
		c    competitorEntry
		want bool
	}{
		{"200+ad", competitorEntry{Name: "KiwiCo", URL: srv.URL + "/ok"}, true},
		{"200 adsız", competitorEntry{Name: "KiwiCo", URL: srv.URL + "/noname"}, false},
		{"404", competitorEntry{Name: "KiwiCo", URL: srv.URL + "/gone"}, false},
		{"http://", competitorEntry{Name: "KiwiCo", URL: strings.Replace(srv.URL, "https://", "http://", 1) + "/ok"}, false},
		{"özel IP'ye çözülen host", competitorEntry{Name: "KiwiCo", URL: "https://evil.test/ok"}, false},
		{"özel IP'ye yönlendirme", competitorEntry{Name: "KiwiCo", URL: srv.URL + "/redir"}, false},
		{"ad yok", competitorEntry{Name: "ab", URL: srv.URL + "/ok"}, false},
	}
	for _, tc := range cases {
		if got := verifyCompetitor(ctx, v, tc.c); got != tc.want {
			t.Errorf("%s: %v beklenirdi, geldi %v", tc.name, tc.want, got)
		}
	}
}

func TestCheckPublicHostAndBlockedIP(t *testing.T) {
	for _, ip := range []string{"10.1.2.3", "172.16.0.1", "172.31.255.1", "192.168.0.1", "127.0.0.1", "169.254.1.1", "100.64.0.1", "100.127.255.1", "::1", "fc00::1", "fd12::1", "fe80::1", "0.0.0.0"} {
		if !isBlockedIP(net.ParseIP(ip)) {
			t.Errorf("%s yasaklı olmalı", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "100.128.0.1", "172.32.0.1", "2606:4700::1111"} {
		if isBlockedIP(net.ParseIP(ip)) {
			t.Errorf("%s serbest olmalı", ip)
		}
	}
	pub := func(ctx context.Context, h string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }
	if err := checkPublicHost(context.Background(), "example.com", pub); err != nil {
		t.Errorf("genel host reddedilmemeli: %v", err)
	}
	if err := checkPublicHost(context.Background(), "8.8.8.8", pub); err == nil {
		t.Error("IP literal host reddedilmeli")
	}
	if err := checkPublicHost(context.Background(), "[::1]", pub); err == nil {
		t.Error("IPv6 literal host reddedilmeli")
	}
}

func testIdea() *store.Idea {
	return &store.Idea{Title: "T", ProblemStatement: "P", ProposedSolution: "S", TargetUser: "U", KnownCompetitorsAIGuess: "X"}
}

func TestRunCompetitorCheck(t *testing.T) {
	srv := newCompetitorTestServer()
	defer srv.Close()
	vf := testVerifier(srv, nil)
	opts := competitorOpts{Verifier: &vf}
	ctx := context.Background()

	two := fmt.Sprintf(`{"slice":"kids kits","competitors":[{"name":"KiwiCo","url":"%s/ok","overlap":"a"},{"name":"MEL Science","url":"%s/ok2","overlap":"b"},{"name":"SonoRev","url":"%s/gone","overlap":"c"}],"verdict":"GAP_TAKEN"}`, srv.URL, srv.URL, srv.URL)
	f := &fakeSearchChat{answer: "Sonuç:\n" + two + "\nbitti"}
	v := runCompetitorCheckWith(ctx, f, testIdea(), opts)
	if v.Verdict != "fail" || v.Lens != competitorLensName || v.PromptVersion != "v2" || v.Subject != "card" || v.Model != "fake-search" {
		t.Errorf("fail beklenirdi: %+v", v)
	}
	if !strings.Contains(v.Reason, "doğrulanmış: KiwiCo ("+srv.URL+"/ok); MEL Science") || !strings.Contains(v.Reason, "doğrulanamayan: SonoRev") || !strings.HasPrefix(v.Reason, "slice: kids kits |") {
		t.Errorf("reason biçimi: %q", v.Reason)
	}
	if f.gotSystem != competitorSystemPrompt {
		t.Error("canlı sistem istemi kullanılmalı")
	}

	one := fmt.Sprintf(`{"slice":"s","competitors":[{"name":"KiwiCo","url":"%s/ok"}],"verdict":"GAP_TAKEN"}`, srv.URL)
	if v := runCompetitorCheckWith(ctx, &fakeSearchChat{answer: one}, testIdea(), opts); v.Verdict != "pass" {
		t.Errorf("1 doğrulanmış -> pass beklenirdi: %+v", v)
	}
	if v := runCompetitorCheckWith(ctx, &fakeSearchChat{answer: "bozuk"}, testIdea(), opts); v.Verdict != "error" {
		t.Errorf("bozuk JSON -> error beklenirdi: %+v", v)
	}
	if v := runCompetitorCheckWith(ctx, &fakeSearchChat{err: errors.New("429")}, testIdea(), opts); v.Verdict != "error" {
		t.Errorf("LLM hatası -> error beklenirdi: %+v", v)
	}
	// SearchChat olmayan istemci -> skipped.
	if v := runCompetitorCheck(ctx, &fakeChat{}, testIdea()); v.Verdict != "skipped" || v.Reason != "istemci arama desteklemiyor" {
		t.Errorf("skipped beklenirdi: %+v", v)
	}
	var _ llm.SearchChat = (*fakeSearchChat)(nil)
}

// Kapalıyken arama çağrısı 0 ve LensVerdicts değişmez; açıkken (error dahil)
// verdict eklenir, kart bloklanmaz (idea döner, eliminations'a dokunulmaz).
func TestApplyCompetitorFlag(t *testing.T) {
	ctx := context.Background()
	f := &fakeSearchChat{answer: `{"slice":"s","competitors":[],"verdict":"GAP_OPEN"}`}

	off := testIdea()
	applyCompetitorFlag(ctx, &config.Config{CompetitorCheck: false}, f, off)
	if f.searchCalls.Load() != 0 || len(off.LensVerdicts) != 0 {
		t.Errorf("kapalıyken arama 0 ve verdict yok beklenirdi: %d, %+v", f.searchCalls.Load(), off.LensVerdicts)
	}

	on := testIdea()
	applyCompetitorFlag(ctx, &config.Config{CompetitorCheck: true}, f, on)
	if f.searchCalls.Load() != 3 || len(on.LensVerdicts) != 1 || on.LensVerdicts[0].Lens != "rakip-kontrolü" || on.LensVerdicts[0].Verdict != "pass" {
		t.Errorf("açıkken pass verdict beklenirdi: %+v", on.LensVerdicts)
	}

	bad := testIdea()
	applyCompetitorFlag(ctx, &config.Config{CompetitorCheck: true}, &fakeSearchChat{answer: "çöp"}, bad)
	if len(bad.LensVerdicts) != 1 || bad.LensVerdicts[0].Verdict != "error" {
		t.Errorf("error verdict da eklenmeli: %+v", bad.LensVerdicts)
	}

	// ctx'teki ayrı arama istemcisi öncelikli; ana istemci arama bilmese bile çalışır.
	sc := &fakeSearchChat{answer: `{"slice":"s","competitors":[]}`}
	viaCtx := testIdea()
	applyCompetitorFlag(WithCompetitorChat(ctx, sc), &config.Config{CompetitorCheck: true}, &fakeChat{}, viaCtx)
	if sc.searchCalls.Load() != 3 || viaCtx.LensVerdicts[0].Verdict != "pass" {
		t.Errorf("ctx istemcisi kullanılmalı: %+v", viaCtx.LensVerdicts)
	}
}

type slowSearchChat struct{ fakeSearchChat }

func (f *slowSearchChat) ChatWithBrowserSearch(ctx context.Context, system, user string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestRunCompetitorCheckTimeout(t *testing.T) {
	v := runCompetitorCheckWith(context.Background(), &slowSearchChat{}, testIdea(), competitorOpts{Timeout: 20 * time.Millisecond})
	if v.Verdict != "error" {
		t.Errorf("zaman aşımı -> error beklenirdi: %+v", v)
	}
}

func TestParseCompetitorAnswerStrict(t *testing.T) {
	for raw, want := range map[string]string{
		`{}`:                                    "error",
		`{"foo":1}`:                             "error",
		`{"slice":"x"}`:                         "error",
		`{"competitors":[]}`:                    "error",
		`{"slice":"x","competitors":[]}`:        "",
		`önce {"slice":"x","competitors":[]} s`: "",
	} {
		if got := parseCompetitorAnswer(raw).Verdict; got != want {
			t.Errorf("%s: %q beklenirdi, geldi %q", raw, want, got)
		}
	}
	f := &fakeSearchChat{answer: `{"slice":"x","competitors":[]}`}
	if v := runCompetitorCheckWith(context.Background(), f, testIdea(), competitorOpts{}); v.Verdict != "pass" {
		t.Errorf("pass beklenirdi: %+v", v)
	}
}
