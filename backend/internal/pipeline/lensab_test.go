package pipeline

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestWriteLensABCSV, YENİ başlığı (#175 madde C: kind + model eklendi,
// reason en sonda) ve satır sırasını doğrular.
func TestWriteLensABCSV(t *testing.T) {
	rows := []LensABRow{
		{ID: 82, Kind: "idea", Lens: "distinctiveness", PromptVersion: "v3", Run: 1, Verdict: "pass", Criterion: "none", Expect: "pass", Match: true, Tokens: 123, Model: "gemini-3.5-flash-lite", Reason: "özgün"},
		{ID: 25, Kind: "elimination", Lens: "third_party", PromptVersion: "v1", Run: 1, Verdict: "fail", Expect: "fail", Match: true, Tokens: 45, Model: "gpt-oss-120b"},
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
	wantHeader := []string{"id", "kind", "lens", "prompt", "run", "verdict", "criterion", "expect", "match", "tokens", "model", "reason"}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Errorf("başlık[%d] = %q, istenen %q", i, records[0][i], h)
		}
	}
	// sütun sırası: id, kind, lens, prompt, run, verdict, criterion, expect, match, tokens, model, reason
	if records[1][0] != "82" || records[1][1] != "idea" || records[1][2] != "distinctiveness" || records[1][9] != "123" || records[1][10] != "gemini-3.5-flash-lite" || records[1][11] != "özgün" {
		t.Errorf("satır 1 yanlış: %v", records[1])
	}
	if records[2][1] != "elimination" || records[2][11] != "" {
		t.Errorf("satır 2 yanlış (kind/boş reason): %v", records[2])
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

// TestGoldenSetRelabeling2026_09, #175 madde F'teki PO yeniden
// etiketlemesinin altın sette uygulandığını doğrular (toplam 158 SABİT —
// yalnız expect/criterion/watch değişti):
//   - distinctiveness: 22/81/84 pass (izlemesiz, kriter yok); 82 + arşiv 8
//     kartı (20,23,85,89,92,93,103,113) fail/K1 (izlemesiz); kalan arşiv 8
//     kartı (34,74,75,80,83,87,91,123) watch=true (expect pass kalır).
//   - third_party/data_access/market_viability: arşivlenen 16 kartın "pass"
//     satırlarına watch=true; 22/81/82/84 DOKUNULMAMIŞ (mevcut
//     expect/expect_any/watch aynen).
func TestGoldenSetRelabeling2026_09(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/lens-golden.json")
	if err != nil {
		t.Fatalf("altın set okunamadı: %v", err)
	}
	var set []GoldenCase
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("altın set JSON değil: %v", err)
	}
	if len(set) != 158 {
		t.Fatalf("F maddesi yalnız etiket değiştirir, toplam SABİT 158 olmalı, geldi: %d", len(set))
	}

	byKey := make(map[string]GoldenCase, len(set))
	for _, gc := range set {
		byKey[fmt.Sprintf("%d/%s", gc.ID, gc.Lens)] = gc
	}
	get := func(id int64, lens string) GoldenCase {
		t.Helper()
		gc, ok := byKey[fmt.Sprintf("%d/%s", id, lens)]
		if !ok {
			t.Fatalf("altın sette bulunamadı: id=%d lens=%s", id, lens)
		}
		return gc
	}

	// distinctiveness: 22/81/84 -> pass, izlemesiz, kriter yok (PO 2026-09-22:
	// yayında kalanlar).
	for _, id := range []int64{22, 81, 84} {
		gc := get(id, "distinctiveness")
		if gc.Expect != "pass" || gc.Watch || gc.Criterion != "" {
			t.Errorf("distinctiveness id=%d: %+v, istenen expect=pass watch=false criterion=''", id, gc)
		}
	}

	// distinctiveness: 82 (PO 2026-09-22: özgünlük elemesi DOĞRU) + arşivlenen
	// 8 kart -> fail/K1, izlemesiz.
	failK1 := []int64{82, 20, 23, 85, 89, 92, 93, 103, 113}
	for _, id := range failK1 {
		gc := get(id, "distinctiveness")
		if gc.Expect != "fail" || gc.Criterion != "K1" || gc.Watch {
			t.Errorf("distinctiveness id=%d: %+v, istenen expect=fail criterion=K1 watch=false", id, gc)
		}
	}

	// distinctiveness: kalan arşivlenen 8 kart -> yalnız watch=true eklendi.
	watchOnly := []int64{34, 74, 75, 80, 83, 87, 91, 123}
	for _, id := range watchOnly {
		gc := get(id, "distinctiveness")
		if gc.Expect != "pass" || !gc.Watch {
			t.Errorf("distinctiveness id=%d: %+v, istenen expect=pass watch=true", id, gc)
		}
	}

	// third_party/data_access/market_viability: arşivlenen 16 kartın (failK1
	// listesindeki 82 HARİÇ 8'i + watchOnly'nin 8'i) "pass" satırlarına
	// watch=true eklendi.
	archived16 := append(append([]int64{}, failK1[1:]...), watchOnly...)
	for _, lens := range []string{"third_party", "data_access", "market_viability"} {
		for _, id := range archived16 {
			gc := get(id, lens)
			if gc.Expect != "pass" || !gc.Watch {
				t.Errorf("%s id=%d: %+v, istenen expect=pass watch=true", lens, id, gc)
			}
		}
	}

	// 22/81/82/84: third_party/data_access/market_viability'de HİÇ
	// DOKUNULMAMIŞ olmalı (spec: "mevcut expect/expect_any/watch aynen kalsın").
	untouched := map[string]GoldenCase{
		"22/third_party":      {ID: 22, Kind: "idea", Lens: "third_party", Expect: "pass"},
		"81/third_party":      {ID: 81, Kind: "idea", Lens: "third_party", Expect: "pass"},
		"82/third_party":      {ID: 82, Kind: "idea", Lens: "third_party", Expect: "pass"},
		"84/third_party":      {ID: 84, Kind: "idea", Lens: "third_party", Expect: "pass"},
		"22/data_access":      {ID: 22, Kind: "idea", Lens: "data_access", Expect: "pass"},
		"81/data_access":      {ID: 81, Kind: "idea", Lens: "data_access", ExpectAny: []string{"pass", "unsure"}},
		"82/data_access":      {ID: 82, Kind: "idea", Lens: "data_access", Expect: "pass"},
		"84/data_access":      {ID: 84, Kind: "idea", Lens: "data_access", Expect: "pass"},
		"22/market_viability": {ID: 22, Kind: "idea", Lens: "market_viability", Expect: "pass"},
		"81/market_viability": {ID: 81, Kind: "idea", Lens: "market_viability", Expect: "pass"},
		"82/market_viability": {ID: 82, Kind: "idea", Lens: "market_viability", Expect: "pass", Watch: true},
		"84/market_viability": {ID: 84, Kind: "idea", Lens: "market_viability", Expect: "pass"},
	}
	for k, want := range untouched {
		got := byKey[k]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s DOKUNULMAMIŞ olmalıydı: geldi %+v, istenen %+v", k, got, want)
		}
	}
}

// TestRunLensABPromptFileRequiresSingleLens, --prompt-file (opts.PromptText
// doluyken) yalnız TEK mercekle (Lens != "all") geçerli olduğunu doğrular —
// bu doğrulama DB/LLM'e HİÇ gitmeden (en erken adımda) yapılır, bu yüzden
// st/chat nil geçilebilir (#175 madde A/G).
func TestRunLensABPromptFileRequiresSingleLens(t *testing.T) {
	set := []GoldenCase{{ID: 1, Kind: "idea", Lens: "third_party", Expect: "pass"}}
	_, err := RunLensAB(context.Background(), nil, nil, set, LensABOptions{
		Lens: "all", PromptText: "aday sistem prompt'u", PromptLabel: "file:aday.txt",
	})
	if err == nil {
		t.Fatal("--prompt-file + --lens=all hata vermeli")
	}
	if !strings.Contains(err.Error(), "prompt-file") {
		t.Errorf("hata mesajı --prompt-file'a değinmeli, geldi: %v", err)
	}
}

// TestLoadLensABResumeState, --resume'un CSV'den mevcut satırları okuyup
// (Watch'ı altın setten eşleştirerek) skip kümesini kurduğunu doğrular:
// verdict="error" satırı ATLANMAZ (tekrar çağrılır), diğeri ATLANIR (#175
// madde G). Gerçek bir dosyaya yazar/okur ama DB/LLM'e hiç dokunmaz.
func TestLoadLensABResumeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.csv")
	content := strings.Join([]string{
		strings.Join(lensABCSVHeader, ","),
		"1,idea,third_party,v1,1,pass,,pass,true,10,gpt-oss-120b,ok",
		"2,idea,third_party,v1,1,error,,pass,false,0,gpt-oss-120b,zaman aşımı",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("yazılamadı: %v", err)
	}

	set := []GoldenCase{
		{ID: 1, Kind: "idea", Lens: "third_party", Expect: "pass", Watch: true},
		{ID: 2, Kind: "idea", Lens: "third_party", Expect: "pass"},
	}
	rows, skip, err := LoadLensABResumeState(path, set)
	if err != nil {
		t.Fatalf("LoadLensABResumeState: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("2 satır beklenirdi, geldi: %d", len(rows))
	}
	if !rows[0].Watch {
		t.Errorf("id=1 satırı altın setten Watch=true almalıydı: %+v", rows[0])
	}
	wantSkip := LensABRowKey{ID: 1, Kind: "idea", Lens: "third_party", PromptVersion: "v1", Run: 1}
	if !skip[wantSkip] {
		t.Errorf("başarılı satır (id=1) skip kümesinde olmalıydı")
	}
	errKey := LensABRowKey{ID: 2, Kind: "idea", Lens: "third_party", PromptVersion: "v1", Run: 1}
	if skip[errKey] {
		t.Errorf("error satırı (id=2) skip kümesinde OLMAMALIYDI (tekrar çağrılmalı)")
	}
}

// TestLoadLensABResumeStateMissingFile, dosya HENÜZ yoksa hata VERMEDEN boş
// durumla dönüldüğünü doğrular (ilk --resume koşusu böyle başlar).
func TestLoadLensABResumeStateMissingFile(t *testing.T) {
	rows, skip, err := LoadLensABResumeState(filepath.Join(t.TempDir(), "yok.csv"), nil)
	if err != nil {
		t.Fatalf("olmayan dosyada hata VERİLMEMELİ: %v", err)
	}
	if len(rows) != 0 || len(skip) != 0 {
		t.Errorf("boş durum beklenirdi: rows=%v skip=%v", rows, skip)
	}
}

// TestLoadLensABResumeStateHeaderMismatch, eski/uyumsuz başlıklı bir CSV'nin
// NET bir hata verdiğini doğrular (#175 madde G: "Başlık uyuşmazsa net hata").
func TestLoadLensABResumeStateHeaderMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eski.csv")
	old := "id,lens,prompt,run,verdict,criterion,expect,match,tokens\n1,third_party,v1,1,pass,,pass,true,10\n"
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatalf("yazılamadı: %v", err)
	}
	if _, _, err := LoadLensABResumeState(path, nil); err == nil {
		t.Fatal("eski (kind/model/reason'sız) başlıkla hata beklenirdi")
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

// newLensABHTTPChat, verilen HTTP handler'ı sunan bir httptest sunucusuna
// karşı GERÇEK bir llm.OpenAICompatClient döner (#175 G testleri —
// 429/rate-limit-dışı hata senaryoları HTTP durum kodu seviyesinde simüle
// edilir; newLensABScriptedChat yalnız hep-200/JSON-verdict senaryolarını
// kapsar). Groq'a/DB'ye dokunmaz, yalnız yerel sunucuya.
func newLensABHTTPChat(t *testing.T, handler http.HandlerFunc) llm.Chat {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &llm.OpenAICompatClient{APIKey: "test", Model: "test-model", BaseURL: srv.URL, HTTPClient: srv.Client()}
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

// TestRunLensABRateLimitRetry, istemcinin KENDİ 4 iç denemesi (maxRetries=3)
// de 429 dönünce RunLensAB'nin opts.RateWait kadar bekleyip AYNI çağrıyı
// yeniden denediğini ve bu seferki başarılı cevabı kullandığını doğrular
// (#175 madde A). Retry-After küçük bir pozitif değere ("0.01") ayarlanır —
// istemcinin kendi iç backoff'u (retryDelay) bunu KULLANIR (sıfırdan
// büyükse üstel geri çekilmeye DÜŞMEZ), testi hızlı tutar.
func TestRunLensABRateLimitRetry(t *testing.T) {
	st, ctx := lensabTestStore(t)

	ideaID, err := st.InsertIdea(ctx, store.Idea{
		Title: "Rate Limit Card", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", ideaID) })

	var calls atomic.Int32
	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 4 { // istemcinin kendi 4 denemesi (maxRetries=3) de 429 dönsün
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"verdict":"pass","criterion":"none","reason":"ok"}`}}},
			"usage":   map[string]any{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	})

	set := []GoldenCase{{ID: ideaID, Kind: "idea", Lens: "third_party", Expect: "pass"}}
	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{
		Lens: "all", PromptVersion: "v1", Runs: 1,
		RateWait: 5 * time.Millisecond, RateRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunLensAB: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("1 satır beklenirdi, geldi: %d", len(result.Rows))
	}
	if result.Rows[0].Verdict != "pass" {
		t.Errorf("429 sonrası yeniden deneme BAŞARILI olmalıydı, satır: %+v", result.Rows[0])
	}
	if got := calls.Load(); got != 5 {
		t.Errorf("istemcinin 4 iç denemesi + lens-ab'nin 1 dış yeniden denemesi = 5 istek beklenirdi, geldi: %d", got)
	}
}

// TestRunLensABRateLimitRetriesExhausted, opts.RateRetries tükenip HÂLÂ 429
// dönerse o (çift,koşu) için "error" satırı yazılıp KOŞUNUN DEVAM ettiğini
// doğrular (#175 madde A).
func TestRunLensABRateLimitRetriesExhausted(t *testing.T) {
	st, ctx := lensabTestStore(t)

	ideaID, err := st.InsertIdea(ctx, store.Idea{
		Title: "Always Rate Limited Card", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", ideaID) })

	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	set := []GoldenCase{{ID: ideaID, Kind: "idea", Lens: "third_party", Expect: "pass"}}
	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{
		Lens: "all", PromptVersion: "v1", Runs: 1,
		RateWait: 2 * time.Millisecond, RateRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunLensAB sürekli 429'da BİLE bitmemeliydi: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Verdict != "error" {
		t.Fatalf("rate-retries tükenince \"error\" satırı beklenirdi: %+v", result.Rows)
	}
	if result.Rows[0].Reason == "" {
		t.Error("error satırının Reason'ı boş olmamalı")
	}
}

// TestRunLensABNonRateLimitErrorContinues, oran-sınırı DIŞI bir LLM hatasının
// (401) koşuyu BİTİRMEDİĞİNİ — o kart için "error" satırı yazılıp sonraki
// karta geçildiğini doğrular (#175 madde A).
func TestRunLensABNonRateLimitErrorContinues(t *testing.T) {
	st, ctx := lensabTestStore(t)

	failID, err := st.InsertIdea(ctx, store.Idea{Title: "Broken Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea (fail): %v", err)
	}
	okID, err := st.InsertIdea(ctx, store.Idea{Title: "OK Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea (ok): %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", failID, okID) })

	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Broken Card") {
			w.WriteHeader(http.StatusUnauthorized) // 401 — retryable DEĞİL, oran sınırı DA DEĞİL
			w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"verdict":"pass","criterion":"none","reason":"ok"}`}}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 1, "total_tokens": 6},
		})
	})

	set := []GoldenCase{
		{ID: failID, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: okID, Kind: "idea", Lens: "third_party", Expect: "pass"},
	}
	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{Lens: "all", PromptVersion: "v1", Runs: 1})
	if err != nil {
		t.Fatalf("RunLensAB oran-sınırı DIŞI hatada BİTMEMELİYDİ: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("2 satır beklenirdi (hata satırı DAHİL), geldi: %d", len(result.Rows))
	}
	if result.Rows[0].Verdict != "error" || result.Rows[0].Reason == "" {
		t.Errorf("ilk kart hata satırı (gerekçeli) olmalıydı: %+v", result.Rows[0])
	}
	if result.Rows[1].Verdict != "pass" {
		t.Errorf("ikinci kart KOŞUYA DEVAM edip başarılı olmalıydı: %+v", result.Rows[1])
	}
}

// TestRunLensABPromptFileLabel, opts.PromptText (--prompt-file) TEK mercekle
// verildiğinde bu METNİN sistem prompt'u olarak GİTTİĞİNİ (server'a giden
// gövdede) ve CSV/özet "prompt" etiketinin PromptLabel olduğunu doğrular
// (#175 madde A/G).
func TestRunLensABPromptFileLabel(t *testing.T) {
	st, ctx := lensabTestStore(t)

	ideaID, err := st.InsertIdea(ctx, store.Idea{Title: "Prompt File Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", ideaID) })

	var gotSystem string
	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				gotSystem = m.Content
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"verdict":"pass","criterion":"none","reason":"ok"}`}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
		})
	})

	set := []GoldenCase{{ID: ideaID, Kind: "idea", Lens: "third_party", Expect: "pass"}}
	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{
		Lens: "third_party", PromptText: "ADAY SİSTEM PROMPT'U — TEST", PromptLabel: "file:aday.txt", Runs: 1,
	})
	if err != nil {
		t.Fatalf("RunLensAB: %v", err)
	}
	if gotSystem != "ADAY SİSTEM PROMPT'U — TEST" {
		t.Errorf("sistem prompt'u PromptText olmalıydı, geldi: %q", gotSystem)
	}
	if len(result.Rows) != 1 || result.Rows[0].PromptVersion != "file:aday.txt" {
		t.Fatalf("prompt etiketi 'file:aday.txt' olmalıydı: %+v", result.Rows)
	}
}

// TestRunLensABResumeSkipsCompletedRuns, RunLensAB'nin opts.SkipKeys'te
// bulunan (id,kind,lens,prompt,run) için LLM'i HİÇ ÇAĞIRMADIĞINI, ama daha
// önce "error" ile biten bir satırı YENİDEN çağırdığını ve opts.ExistingRows'un
// özet hesabına (Summaries) eski+yeni BİRLİKTE girdiğini doğrular (#175 G).
func TestRunLensABResumeSkipsCompletedRuns(t *testing.T) {
	st, ctx := lensabTestStore(t)

	doneID, err := st.InsertIdea(ctx, store.Idea{Title: "Resume Done Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea (done): %v", err)
	}
	retryID, err := st.InsertIdea(ctx, store.Idea{Title: "Resume Retry Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea (retry): %v", err)
	}
	newID, err := st.InsertIdea(ctx, store.Idea{Title: "Resume New Card", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea (new): %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2, $3)", doneID, retryID, newID) })

	var calls atomic.Int32
	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"verdict":"pass","criterion":"none","reason":"ok"}`}}},
			"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5},
		})
	})

	set := []GoldenCase{
		{ID: doneID, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: retryID, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: newID, Kind: "idea", Lens: "third_party", Expect: "pass"},
	}
	existing := []LensABRow{
		{ID: doneID, Kind: "idea", Lens: "third_party", PromptVersion: "v1", Run: 1, Verdict: "pass", Expect: "pass", Match: true, Tokens: 9},
		{ID: retryID, Kind: "idea", Lens: "third_party", PromptVersion: "v1", Run: 1, Verdict: "error", Expect: "pass", Match: false, Tokens: 0, Reason: "önceki koşu hata verdi"},
	}
	skip := map[LensABRowKey]bool{
		{ID: doneID, Kind: "idea", Lens: "third_party", PromptVersion: "v1", Run: 1}: true,
		// retryID SkipKeys'te YOK (önceki satır "error") -> yeniden çağrılmalı.
	}

	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{
		Lens: "all", PromptVersion: "v1", Runs: 1, ExistingRows: existing, SkipKeys: skip,
	})
	if err != nil {
		t.Fatalf("RunLensAB: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("yalnız retryID+newID için 2 LLM çağrısı beklenirdi (doneID atlanmalıydı), geldi: %d", got)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("Rows yalnız YENİ üretilen 2 satırı içermeli (ExistingRows tekrarlanmaz): %+v", result.Rows)
	}

	var tp *LensABLensSummary
	for i := range result.Summaries {
		if result.Summaries[i].Lens == "third_party" {
			tp = &result.Summaries[i]
		}
	}
	if tp == nil || tp.Cases != 3 || tp.Matches != 3 {
		t.Fatalf("özet ESKİ+YENİ satırları BİRLİKTE saymalı (3 kart, 3 uyum — retryID'nin eski hata satırı DEĞİL yeni pass satırı sayılmalı): %+v", tp)
	}
}

// TestRunLensABOnRowStreamsImmediately, opts.OnRow'un HER satır
// üretildiğinde (bir sonraki üretilmeden önce) çağrıldığını ve bir ara
// yazma hatasında ÜRETİLMİŞ satırların (result.Rows) KORUNDUĞUNU doğrular
// (#175 madde B: süreç ortada ölse de o ana kadarki satırlar dosyada kalır).
func TestRunLensABOnRowStreamsImmediately(t *testing.T) {
	st, ctx := lensabTestStore(t)

	idA, err := st.InsertIdea(ctx, store.Idea{Title: "Stream Card A", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea A: %v", err)
	}
	idB, err := st.InsertIdea(ctx, store.Idea{Title: "Stream Card B", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", UrgencyScore: 3})
	if err != nil {
		t.Fatalf("InsertIdea B: %v", err)
	}
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", idA, idB) })

	chat := newLensABHTTPChat(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"verdict":"pass","criterion":"none","reason":"ok"}`}}},
			"usage":   map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		})
	})

	set := []GoldenCase{
		{ID: idA, Kind: "idea", Lens: "third_party", Expect: "pass"},
		{ID: idB, Kind: "idea", Lens: "third_party", Expect: "pass"},
	}

	var streamed []LensABRow
	writeErr := errors.New("disk dolu (test)")
	result, err := RunLensAB(ctx, st, chat, set, LensABOptions{
		Lens: "all", PromptVersion: "v1", Runs: 1,
		OnRow: func(r LensABRow) error {
			streamed = append(streamed, r)
			if len(streamed) == 2 {
				return writeErr
			}
			return nil
		},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("2. satırda OnRow hatası RunLensAB'den AYNEN dönmeliydi, geldi: %v", err)
	}
	if len(streamed) != 2 {
		t.Fatalf("OnRow tam olarak 2 kez (2. satırda durmalı) çağrılmalıydı, geldi: %d", len(streamed))
	}
	if len(result.Rows) != 2 {
		t.Fatalf("hataya rağmen ÜRETİLMİŞ 2 satır result.Rows'ta KORUNMALIYDI, geldi: %d", len(result.Rows))
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
