package pipeline

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

// ---------------------------------------------------------------- saf fonksiyon testleri (DB gerekmez)

func TestMatchGoldenCase(t *testing.T) {
	tests := []struct {
		name string
		gc   GoldenCase
		v    lensVerdict
		want bool
	}{
		{"tek expect eşleşir", GoldenCase{Expect: "pass"}, lensVerdict{Verdict: "pass"}, true},
		{"tek expect eşleşmez", GoldenCase{Expect: "pass"}, lensVerdict{Verdict: "fail"}, false},
		{"expect_any içinde", GoldenCase{ExpectAny: []string{"pass", "unsure"}}, lensVerdict{Verdict: "unsure"}, true},
		{"expect_any dışında", GoldenCase{ExpectAny: []string{"pass", "unsure"}}, lensVerdict{Verdict: "fail"}, false},
		{"expect boş", GoldenCase{}, lensVerdict{Verdict: "pass"}, false},
		{"kriter eşleşir", GoldenCase{Expect: "fail", Criterion: "K1"}, lensVerdict{Verdict: "fail", Criterion: "K1"}, true},
		{"kriter eşleşmez -> uyumsuzluk", GoldenCase{Expect: "fail", Criterion: "K1"}, lensVerdict{Verdict: "fail", Criterion: "K2"}, false},
		{"kriter yalnız fail'de kontrol edilir", GoldenCase{ExpectAny: []string{"pass", "unsure"}, Criterion: "K1"}, lensVerdict{Verdict: "unsure", Criterion: "none"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchGoldenCase(tt.gc, tt.v); got != tt.want {
				t.Errorf("matchGoldenCase(%+v, %+v) = %v, istenen %v", tt.gc, tt.v, got, tt.want)
			}
		})
	}
}

func TestExpectLabel(t *testing.T) {
	if got := expectLabel(GoldenCase{Expect: "pass"}); got != "pass" {
		t.Errorf("expectLabel: %q", got)
	}
	if got := expectLabel(GoldenCase{ExpectAny: []string{"pass", "unsure"}}); got != "pass|unsure" {
		t.Errorf("expectLabel expect_any: %q", got)
	}
}

// TestBuildLensSummaries, uyum/tekrarlanabilirlik hesabını GERÇEK bir LLM/DB
// çağrısı olmadan, önceden üretilmiş satırlar üzerinden doğrular (#165 kabul
// kriteri: "sahte chat ile uyum/tekrar hesabı").
func TestBuildLensSummaries(t *testing.T) {
	rows := []LensABRow{
		// third_party: "Pass Card" iki koşuda da pass (match+tekrarlanabilir).
		{ID: 1, Kind: "idea", Lens: "third_party", Run: 1, Verdict: "pass", Expect: "pass", Match: true, Tokens: 10},
		{ID: 1, Kind: "idea", Lens: "third_party", Run: 2, Verdict: "pass", Expect: "pass", Match: true, Tokens: 10},
		// third_party: "Fail Card" iki koşuda da pass ama expect fail -> uyumsuz, ama tekrarlanabilir.
		{ID: 2, Kind: "idea", Lens: "third_party", Run: 1, Verdict: "pass", Expect: "fail", Match: false, Tokens: 12},
		{ID: 2, Kind: "idea", Lens: "third_party", Run: 2, Verdict: "pass", Expect: "fail", Match: false, Tokens: 12},
		// data_access: eleme satırı, run1 fail (match), run2 unsure (tekrarlanamaz).
		{ID: 9, Kind: "elimination", Lens: "data_access", Run: 1, Verdict: "fail", Expect: "fail", Match: true, Tokens: 8},
		{ID: 9, Kind: "elimination", Lens: "data_access", Run: 2, Verdict: "unsure", Expect: "fail", Match: false, Tokens: 8},
		// data_access: izleme satırı — uyuma girmemeli.
		{ID: 5, Kind: "elimination", Lens: "data_access", Run: 1, Verdict: "pass", Expect: "pass", Match: true, Watch: true, Tokens: 6},
	}

	summaries := buildLensSummaries([]string{"third_party", "data_access"}, rows)
	if len(summaries) != 2 {
		t.Fatalf("2 mercek özeti beklenirdi, geldi: %d", len(summaries))
	}

	tp := summaries[0]
	if tp.Lens != "third_party" || tp.Cases != 2 || tp.Matches != 1 {
		t.Errorf("third_party özet yanlış: %+v", tp)
	}
	if tp.MatchPct != 50 {
		t.Errorf("third_party uyum yüzdesi: %.1f, istenen 50", tp.MatchPct)
	}
	if tp.RepeatableTotal != 2 || tp.RepeatableCases != 2 || tp.RepeatablePct != 100 {
		t.Errorf("third_party tekrarlanabilirlik yanlış: %+v", tp)
	}
	if tp.TotalTokens != 44 {
		t.Errorf("third_party token toplamı: %d, istenen 44", tp.TotalTokens)
	}

	da := summaries[1]
	if da.Lens != "data_access" || da.Cases != 1 || da.Matches != 1 {
		t.Errorf("data_access özet yanlış: %+v", da)
	}
	if da.RepeatableTotal != 1 || da.RepeatableCases != 0 {
		t.Errorf("data_access tekrarlanabilirlik yanlış (fail!=unsure): %+v", da)
	}
	if da.WatchCases != 1 || da.WatchMatch != 1 {
		t.Errorf("data_access izleme sayacı yanlış: %+v", da)
	}
	if da.TotalTokens != 22 {
		t.Errorf("data_access token toplamı: %d, istenen 22 (8+8+6)", da.TotalTokens)
	}
}

func TestWriteLensABCSV(t *testing.T) {
	rows := []LensABRow{
		{ID: 82, Lens: "distinctiveness", PromptVersion: "v3", Run: 1, Verdict: "pass", Criterion: "none", Expect: "pass", Match: true, Tokens: 123},
		{ID: 25, Lens: "third_party", PromptVersion: "v1", Run: 1, Verdict: "fail", Expect: "fail", Match: true, Tokens: 45},
	}
	var buf strings.Builder
	if err := WriteLensABCSV(&buf, rows); err != nil {
		t.Fatalf("WriteLensABCSV: %v", err)
	}
	r := csv.NewReader(strings.NewReader(buf.String()))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv okunamadı: %v", err)
	}
	if len(records) != 3 { // başlık + 2 satır
		t.Fatalf("3 satır beklenirdi (başlık+2), geldi: %d", len(records))
	}
	wantHeader := []string{"id", "lens", "prompt", "run", "verdict", "criterion", "expect", "match", "tokens"}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Errorf("başlık[%d] = %q, istenen %q", i, records[0][i], h)
		}
	}
	if records[1][0] != "82" || records[1][1] != "distinctiveness" || records[1][8] != "123" {
		t.Errorf("satır 1 yanlış: %v", records[1])
	}
}

// TestGoldenSetFile, testdata/lens-golden.json'un #165 §5'teki tabloyla
// birebir eşleştiğini (mercek başına çift sayısı + toplam 158) doğrular —
// JSON'un kendisini okuyup ayrıştırma testi.
func TestGoldenSetFile(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/lens-golden.json")
	if err != nil {
		t.Fatalf("altın set okunamadı: %v", err)
	}
	var set []GoldenCase
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("altın set JSON değil: %v", err)
	}
	if len(set) != 158 {
		t.Fatalf("toplam 158 çift beklenirdi, geldi: %d", len(set))
	}

	wantCounts := map[string]int{
		"third_party":      33,
		"data_access":      38,
		"market_viability": 35,
		"distinctiveness":  52,
	}
	got := map[string]int{}
	for _, gc := range set {
		if gc.Kind != "idea" && gc.Kind != "elimination" {
			t.Errorf("id=%d bilinmeyen kind: %q", gc.ID, gc.Kind)
		}
		if _, ok := lensRegistry[gc.Lens]; !ok {
			t.Errorf("id=%d bilinmeyen mercek: %q", gc.ID, gc.Lens)
		}
		if gc.Expect == "" && len(gc.ExpectAny) == 0 {
			t.Errorf("id=%d (%s/%s) expect/expect_any boş", gc.ID, gc.Kind, gc.Lens)
		}
		if gc.Criterion != "" && gc.Lens != "distinctiveness" {
			t.Errorf("id=%d (%s) yalnız distinctiveness'ta criterion olmalı", gc.ID, gc.Lens)
		}
		got[gc.Lens]++
	}
	for lens, want := range wantCounts {
		if got[lens] != want {
			t.Errorf("mercek %q: %d çift beklenirdi, geldi: %d", lens, want, got[lens])
		}
	}
}

// ---------------------------------------------------------------- DB gerektiren uçtan uca testler

// lensabTestStore, gerçek DB isteyen lens-ab testleri için ortak kurulum
// (TEST_DATABASE_URL yoksa atlanır — retheme_test.go/scrub_test.go'daki
// desenle aynı).
func lensabTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)
	return st, ctx
}

// scriptedChatResponse, newLensABScriptedChat'in kullanıcı promptundaki bir
// alt dizeye (title/subject) göre koşu bazlı verdict/kriter/token döndürdüğü
// senaryo tanımı.
type scriptedChatResponse struct {
	contains       string
	verdictByRun   []string // index 0 = run 1, index 1 = run 2, ...
	criterionByRun []string
	tokensPerCall  int
}

// newLensABScriptedChat, GERÇEK llm.OpenAICompatClient'ı yerel bir httptest
// sunucusuna karşı döner (#144 usage_stage_test.go'daki desenle AYNI — el
// yazımı sahte Chat'ler llm.UsageMeter'ı hiç tetiklemez, RunLensAB'nin
// token sayımını doğrulamak için GERÇEK istemci şart). Groq'a/DB'ye
// dokunmaz, yalnız yerel sunucuya.
func newLensABScriptedChat(t *testing.T, scripts []scriptedChatResponse) llm.Chat {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &req)
		var user string
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}

		var sc *scriptedChatResponse
		for i := range scripts {
			if strings.Contains(user, scripts[i].contains) {
				sc = &scripts[i]
				break
			}
		}
		if sc == nil {
			t.Errorf("script bulunamadı, user prompt: %.100s", user)
			return
		}

		mu.Lock()
		calls[sc.contains]++
		idx := calls[sc.contains] - 1
		mu.Unlock()

		verdict := "unsure"
		if idx < len(sc.verdictByRun) {
			verdict = sc.verdictByRun[idx]
		}
		criterion := ""
		if idx < len(sc.criterionByRun) {
			criterion = sc.criterionByRun[idx]
		}

		content, _ := json.Marshal(map[string]string{"verdict": verdict, "criterion": criterion, "reason": "test"})
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": string(content)}}},
			"usage": map[string]any{
				"prompt_tokens": sc.tokensPerCall - 2, "completion_tokens": 2, "total_tokens": sc.tokensPerCall,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return &llm.OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
}

// TestRunLensABEndToEnd, RunLensAB'nin DB'den idea/elimination metnini
// çektiğini, iki koşuda uyum+tekrarlanabilirlik hesabını doğru yaptığını ve
// llm.UsageMeter üzerinden GERÇEK token sayımı yaptığını (#144 entegrasyonu)
// doğrular. HİÇBİR ŞEY YAZMAZ (yalnız InsertIdea/InsertElimination testin
// kendi kurulumu — RunLensAB'nin kendisi salt okur).
func TestRunLensABEndToEnd(t *testing.T) {
	st, ctx := lensabTestStore(t)

	passID, err := st.InsertIdea(ctx, store.Idea{
		Title: "Pass Card Lens AB Test", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea (pass): %v", err)
	}
	failID, err := st.InsertIdea(ctx, store.Idea{
		Title: "Fail Card Lens AB Test", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea (fail): %v", err)
	}
	elimID, err := st.InsertElimination(ctx, store.Elimination{
		Stage: "blocking_lens", Subject: "Elim Subject Lens AB Test", Verdict: "fail",
	})
	if err != nil {
		t.Fatalf("InsertElimination: %v", err)
	}
	t.Cleanup(func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", passID, failID)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE id = $1", elimID)
	})

	chat := newLensABScriptedChat(t, []scriptedChatResponse{
		{contains: "Pass Card Lens AB Test", verdictByRun: []string{"pass", "pass"}, tokensPerCall: 10},
		{contains: "Fail Card Lens AB Test", verdictByRun: []string{"pass", "pass"}, tokensPerCall: 12},
		{contains: "Elim Subject Lens AB Test", verdictByRun: []string{"fail", "unsure"}, tokensPerCall: 8},
	})

	set := []GoldenCase{
		{ID: passID, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: failID, Kind: "idea", Lens: "third_party", Expect: "fail"},
		{ID: elimID, Kind: "elimination", Lens: "data_access", Expect: "fail"},
	}

	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{Lens: "all", PromptVersion: "v1", Runs: 2})
	if err != nil {
		t.Fatalf("RunLensAB: %v", err)
	}
	if len(result.Rows) != 6 {
		t.Fatalf("6 satır (3 çift x 2 koşu) beklenirdi, geldi: %d", len(result.Rows))
	}
	if result.TotalTokens != 60 { // (10+10)+(12+12)+(8+8)
		t.Errorf("toplam token: %d, istenen 60", result.TotalTokens)
	}
	if result.BudgetExceeded {
		t.Error("bütçe verilmedi, aşılmamalı")
	}

	var tp, da *LensABLensSummary
	for i := range result.Summaries {
		switch result.Summaries[i].Lens {
		case "third_party":
			tp = &result.Summaries[i]
		case "data_access":
			da = &result.Summaries[i]
		}
	}
	if tp == nil || da == nil {
		t.Fatalf("beklenen mercek özetleri eksik: %+v", result.Summaries)
	}
	if tp.Cases != 2 || tp.Matches != 1 {
		t.Errorf("third_party özet: %+v", *tp)
	}
	if tp.RepeatableCases != 2 || tp.RepeatableTotal != 2 {
		t.Errorf("third_party tekrarlanabilirlik: %+v", *tp)
	}
	if da.Cases != 1 || da.Matches != 1 || da.RepeatableCases != 0 {
		t.Errorf("data_access özet: %+v", *da)
	}
}

// TestRunLensABBudgetStop, birikimli token bütçesi aşıldığında RunLensAB'nin
// kalan çiftleri ÇAĞIRMADAN durduğunu ve kaldığı indeksi döndürdüğünü
// doğrular (#165 kabul kriteri).
func TestRunLensABBudgetStop(t *testing.T) {
	st, ctx := lensabTestStore(t)

	idA, err := st.InsertIdea(ctx, store.Idea{Title: "Budget Card A", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea A: %v", err)
	}
	idB, err := st.InsertIdea(ctx, store.Idea{Title: "Budget Card B", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea B: %v", err)
	}
	idC, err := st.InsertIdea(ctx, store.Idea{Title: "Budget Card C", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea C: %v", err)
	}
	t.Cleanup(func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2, $3)", idA, idB, idC)
	})

	chat := newLensABScriptedChat(t, []scriptedChatResponse{
		{contains: "Budget Card A", verdictByRun: []string{"pass"}, tokensPerCall: 50},
		{contains: "Budget Card B", verdictByRun: []string{"pass"}, tokensPerCall: 50},
		{contains: "Budget Card C", verdictByRun: []string{"pass"}, tokensPerCall: 50},
	})

	set := []GoldenCase{
		{ID: idA, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: idB, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: idC, Kind: "idea", Lens: "third_party", Expect: "pass"},
	}

	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{Lens: "all", PromptVersion: "v1", Runs: 1, BudgetTokens: 80})
	if err != nil {
		t.Fatalf("RunLensAB: %v", err)
	}
	if !result.BudgetExceeded {
		t.Fatal("bütçe aşılmalıydı")
	}
	if len(result.Rows) != 2 {
		t.Fatalf("bütçe 2. çiftte aşılır (2x50=100>=80), 2 satır beklenirdi, geldi: %d", len(result.Rows))
	}
	if result.StoppedAtCase != 1 {
		t.Errorf("StoppedAtCase = %d, istenen 1 (0-tabanlı, ikinci çift)", result.StoppedAtCase)
	}
	if result.StoppedAtRun != 1 {
		t.Errorf("StoppedAtRun = %d, istenen 1", result.StoppedAtRun)
	}
	if result.TotalTokens != 100 {
		t.Errorf("toplam token: %d, istenen 100", result.TotalTokens)
	}
}

// TestLensABUserPromptFromDB, lensABUserPrompt'un idea/elimination
// kayıtlarını DB'den doğru çektiğini doğrular (#165 kabul kriteri: "DB
// testi: idea/elimination metni çekme").
func TestLensABUserPromptFromDB(t *testing.T) {
	st, ctx := lensabTestStore(t)

	ideaID, err := st.InsertIdea(ctx, store.Idea{
		Title: "Prompt Test Card", ProblemStatement: "sorun metni", ProposedSolution: "çözüm metni",
		TargetUser: "hedef kullanıcı", SourceType: "pain_point", UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	detail := "eleme detay metni"
	elimID, err := st.InsertElimination(ctx, store.Elimination{
		Stage: "blocking_lens", Subject: "Prompt Test Elim", Verdict: "fail", Detail: &detail,
	})
	if err != nil {
		t.Fatalf("InsertElimination: %v", err)
	}
	t.Cleanup(func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", ideaID)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE id = $1", elimID)
	})

	ideaPrompt, err := lensABUserPrompt(ctx, st, GoldenCase{ID: ideaID, Kind: "idea"})
	if err != nil {
		t.Fatalf("lensABUserPrompt (idea): %v", err)
	}
	want := ideaLensUserPrompt("Prompt Test Card", "sorun metni", "çözüm metni", "hedef kullanıcı")
	if ideaPrompt != want {
		t.Errorf("idea prompt = %q, istenen %q", ideaPrompt, want)
	}

	elimPrompt, err := lensABUserPrompt(ctx, st, GoldenCase{ID: elimID, Kind: "elimination"})
	if err != nil {
		t.Fatalf("lensABUserPrompt (elimination): %v", err)
	}
	wantElim := ideaLensUserPrompt("Prompt Test Elim", "eleme detay metni", "", "")
	if elimPrompt != wantElim {
		t.Errorf("elimination prompt = %q, istenen %q", elimPrompt, wantElim)
	}

	if _, err := lensABUserPrompt(ctx, st, GoldenCase{ID: 999999999, Kind: "idea"}); err != store.ErrNotFound {
		t.Errorf("var olmayan idea: ErrNotFound bekleniyordu, geldi: %v", err)
	}
}

// TestRunLensABUnknownLensAndPrompt, geçersiz --lens/--prompt değerlerinin
// hata döndürdüğünü doğrular — DB'ye hiç dokunmaz (ilk doğrulamada döner).
func TestRunLensABUnknownLensAndPrompt(t *testing.T) {
	st, ctx := lensabTestStore(t)
	chat := newLensABScriptedChat(t, nil)

	if _, err := RunLensAB(ctx, st, chat, nil, LensABOptions{Lens: "all", PromptVersion: "v2"}); err == nil {
		t.Error("geçersiz --prompt hata vermeli")
	}
	if _, err := RunLensAB(ctx, st, chat, nil, LensABOptions{Lens: "bilinmeyen", PromptVersion: "v1"}); err == nil {
		t.Error("geçersiz --lens hata vermeli")
	}
}
