package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// seqSearchChat: her çağrıda sıradaki cevabı/hatayı döner (v2 oylama testi).
type seqSearchChat struct {
	answers []string
	errs    []error
	calls   atomic.Int32
}

func (f *seqSearchChat) ChatJSON(ctx context.Context, s, u string) (string, error) { return "{}", nil }
func (f *seqSearchChat) ChatJSONWithTemperature(ctx context.Context, s, u string, t float64) (string, error) {
	return "{}", nil
}
func (f *seqSearchChat) ChatWithBrowserSearch(ctx context.Context, system, user string) (string, error) {
	i := int(f.calls.Add(1)) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		return "", f.errs[i]
	}
	return f.answers[i], nil
}

func compAns(srvURL string, entries ...string) string {
	var parts []string
	for _, e := range entries { // "Ad|yol"
		p := strings.SplitN(e, "|", 2)
		parts = append(parts, fmt.Sprintf(`{"name":%q,"url":"%s%s"}`, p[0], srvURL, p[1]))
	}
	return `{"slice":"s","competitors":[` + strings.Join(parts, ",") + `],"verdict":"GAP_TAKEN"}`
}

func TestRunCompetitorCheckVoting(t *testing.T) {
	srv := newCompetitorTestServer()
	defer srv.Close()
	vf := testVerifier(srv, nil)
	ctx := context.Background()
	opts := competitorOpts{Verifier: &vf} // N=3 varsayılan

	// Farklı aramalardan 1+1 farklı doğrulanmış rakip -> fail; 3 çağrı.
	f := &seqSearchChat{answers: []string{compAns(srv.URL, "KiwiCo|/ok"), compAns(srv.URL, "MEL Science|/ok2"), compAns(srv.URL)}}
	v := runCompetitorCheckWith(ctx, f, testIdea(), opts)
	if f.calls.Load() != 3 || v.Verdict != "fail" || !strings.Contains(v.Reason, "aramalar: 3/3 başarılı") {
		t.Errorf("fail + 3 çağrı beklenirdi: calls=%d %+v", f.calls.Load(), v)
	}

	// Aynı rakip (büyük/küçük harf farkıyla) iki aramada -> 1 sayılır -> pass.
	f = &seqSearchChat{answers: []string{compAns(srv.URL, "KiwiCo|/ok"), compAns(srv.URL, "kiwico|/ok"), compAns(srv.URL)}}
	if v := runCompetitorCheckWith(ctx, f, testIdea(), opts); v.Verdict != "pass" {
		t.Errorf("aynı rakip 1 sayılmalı -> pass: %+v", v)
	}

	// 2 error + 1 başarılı -> başarılıyla karar (2 doğrulanmış -> fail).
	f = &seqSearchChat{
		errs:    []error{errors.New("429"), errors.New("429"), nil},
		answers: []string{"", "", compAns(srv.URL, "KiwiCo|/ok", "MEL Science|/ok2")},
	}
	v = runCompetitorCheckWith(ctx, f, testIdea(), opts)
	if v.Verdict != "fail" || !strings.Contains(v.Reason, "aramalar: 1/3 başarılı") {
		t.Errorf("başarılı aramayla karar beklenirdi: %+v", v)
	}

	// 3 error -> error.
	f = &seqSearchChat{errs: []error{errors.New("a"), errors.New("b"), errors.New("c")}, answers: []string{"", "", ""}}
	v = runCompetitorCheckWith(ctx, f, testIdea(), opts)
	if v.Verdict != "error" || !strings.Contains(v.Reason, "0/3") {
		t.Errorf("3 hata -> error beklenirdi: %+v", v)
	}

	// Searches=1 -> tek çağrı; Searches=9 -> 5'e kırpılır.
	f1 := &fakeSearchChat{answer: `{"slice":"s","competitors":[]}`}
	runCompetitorCheckWith(ctx, f1, testIdea(), competitorOpts{Verifier: &vf, Searches: 1})
	if f1.searchCalls.Load() != 1 {
		t.Errorf("1 çağrı beklenirdi: %d", f1.searchCalls.Load())
	}
	f9 := &fakeSearchChat{answer: `{"slice":"s","competitors":[]}`}
	runCompetitorCheckWith(ctx, f9, testIdea(), competitorOpts{Verifier: &vf, Searches: 9})
	if f9.searchCalls.Load() != 5 {
		t.Errorf("5 çağrı beklenirdi: %d", f9.searchCalls.Load())
	}
}

// Birleşik listeden en çok competitorMaxVerify rakip doğrulanır.
func TestRunCompetitorCheckMaxVerify(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "alpha beta") })
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	vf := testVerifier(srv, nil)
	var es []string
	for i := 0; i < 12; i++ {
		es = append(es, fmt.Sprintf("alpha%c|/p%d", 'a'+i, i))
	}
	f := &fakeSearchChat{answer: compAns(srv.URL, es...)}
	runCompetitorCheckWith(context.Background(), f, testIdea(), competitorOpts{Verifier: &vf, Searches: 2})
	if hits.Load() > competitorMaxVerify {
		t.Errorf("en fazla %d doğrulama beklenirdi: %d", competitorMaxVerify, hits.Load())
	}
}
