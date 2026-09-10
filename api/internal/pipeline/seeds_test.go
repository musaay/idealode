package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/store"
)

func TestParseRadarSeedsSkipsMalformed(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"date":"2026-01-01","name":"Seed A","summary":"s","evidence":"e","source_url":"https://a.example","tr_angle":"t"}`,
		`not json at all`,
		`{"name":"","source_url":"https://missing-name.example"}`,
		`{"date":"2026-01-02","name":"Seed B","summary":"s2","evidence":"e2","source_url":"https://b.example","tr_angle":"t2"}`,
		``,
	}, "\n")

	seeds := parseRadarSeeds(jsonl)
	if len(seeds) != 2 {
		t.Fatalf("2 geçerli tohum beklenirdi, geldi: %d (%+v)", len(seeds), seeds)
	}
	if seeds[0].Name != "Seed A" || seeds[1].Name != "Seed B" {
		t.Errorf("beklenmeyen tohum sırası/adı: %+v", seeds)
	}
}

// TestParseLensVerdict, özgünlük merceği cevabının savunmacı ayrıştırılmasını
// doğrular — K5 "heyecan" kriteri de K1-K4 gibi tanınmalı (#114), tanınmayan
// bir criterion değeri "none"a indirgenmeli.
func TestParseLensVerdict(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		wantVerdict   string
		wantCriterion string
	}{
		{
			name:          "K5 tanınır",
			raw:           `{"verdict":"fail","criterion":"K5","reason":"bilinen mekaniğin kopyası"}`,
			wantVerdict:   "fail",
			wantCriterion: "K5",
		},
		{
			name:          "K1-K4 hâlâ tanınır",
			raw:           `{"verdict":"fail","criterion":"K4","reason":"kırılgan"}`,
			wantVerdict:   "fail",
			wantCriterion: "K4",
		},
		{
			name:          "tanınmayan criterion none'a iner",
			raw:           `{"verdict":"fail","criterion":"K9","reason":"?"}`,
			wantVerdict:   "fail",
			wantCriterion: "none",
		},
		{
			name:          "pass'te criterion none",
			raw:           `{"verdict":"pass","criterion":"none","reason":"ok"}`,
			wantVerdict:   "pass",
			wantCriterion: "none",
		},
		{
			name:          "JSON değilse unsure",
			raw:           `not json`,
			wantVerdict:   "unsure",
			wantCriterion: "none",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLensVerdict(tc.raw)
			if got.Verdict != tc.wantVerdict || got.Criterion != tc.wantCriterion {
				t.Errorf("parseLensVerdict(%q) = {%q, %q}, want {%q, %q}",
					tc.raw, got.Verdict, got.Criterion, tc.wantVerdict, tc.wantCriterion)
			}
		})
	}
}

// fakeSeedChat, ProcessSeeds entegrasyon testleri için sistem prompt'una
// göre sabit cevap döner: 3 merceğe lensVerdict, kart üretimine
// cardResponse, dedup hakemine (gerekirse) dupSame.
type fakeSeedChat struct {
	lensVerdict  string
	cardResponse string
	dupSame      bool

	// lastTemp, sıcaklık politikasını (#106) doğrulayan testler için son
	// çağrının sıcaklığını sistem prompt'una göre kaydeder.
	lastTemp map[string]float64
}

func (f *fakeSeedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *fakeSeedChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if f.lastTemp == nil {
		f.lastTemp = map[string]float64{}
	}
	f.lastTemp[system] = temp
	switch {
	case system == dupJudgeSystem:
		return fmt.Sprintf(`{"same": %v}`, f.dupSame), nil
	case strings.Contains(system, `"market_derived"`):
		return f.cardResponse, nil
	default:
		return fmt.Sprintf(`{"verdict":%q,"reason":"test-reason"}`, f.lensVerdict), nil
	}
}

func seedTestStore(t *testing.T) *store.Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	st, err := store.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestProcessSeedsPassCreatesCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-pass"
	title := "Test Tohum Fikri"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Pass Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)

	chat := &fakeSeedChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 1 {
		t.Fatalf("1 idea beklenirdi, geldi: %d", n)
	}

	var sourceType string
	var evidenceCount int
	var quotes []string
	if err := st.Pool.QueryRow(ctx,
		"SELECT source_type, evidence_count, example_quotes FROM ideas WHERE title = $1", title).
		Scan(&sourceType, &evidenceCount, &quotes); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if sourceType != "market_derived" || evidenceCount != 1 {
		t.Errorf("source_type=market_derived, evidence_count=1 beklenirdi: %s, %d", sourceType, evidenceCount)
	}
	if len(quotes) != 1 || !strings.Contains(quotes[0], "Pass Seed") || !strings.Contains(quotes[0], seedURL) {
		t.Errorf("example_quotes override edilmemiş (LLM'den değil, koddan gelmeli): %v", quotes)
	}

	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Errorf("raw_posts işaret satırı bulunamadı")
	}

	// Sıcaklık politikası (#106): 3 bloklayıcı mercek yargı=0, kart üretimi
	// (LLM sistem prompt'unda "market_derived" geçer) üretim=0.3.
	if len(chat.lastTemp) < 4 {
		t.Fatalf("beklenen çağrı sayısına ulaşılmadı (3 mercek + kart üretimi): %d", len(chat.lastTemp))
	}
	for sys, temp := range chat.lastTemp {
		if strings.Contains(sys, `"market_derived"`) {
			if temp != 0.3 {
				t.Errorf("kart üretimi sıcaklık 0.3 olmalı, geldi: %v", temp)
			}
			continue
		}
		if temp != 0 {
			t.Errorf("mercek çağrısı sıcaklık 0 olmalı, geldi: %v", temp)
		}
	}

	// İkinci koşu aynı tohumu tekrar işlememeli (idempotency).
	n2, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (2. koşu): %v", err)
	}
	if n2 != 0 {
		t.Errorf("ikinci koşu 0 idea üretmeli (zaten işlenmiş), geldi: %d", n2)
	}
}

func TestProcessSeedsFailMarksNoCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-fail"
	title := "Test Elenen Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Fail Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)

	chat := &fakeSeedChat{
		lensVerdict: "fail",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 0 {
		t.Fatalf("0 idea beklenirdi (elendi), geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Errorf("elenen tohumdan kart üretilmemeli")
	}

	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Errorf("elenen tohum da raw_posts'a işaretlenmeli (yeniden işlenmesin)")
	}
}

// errChat, her çağrıda hata döner (ağ/kota kesintisi simülasyonu).
type errChat struct{}

func (errChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return "", fmt.Errorf("simulated LLM hatası")
}

func (errChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	return "", fmt.Errorf("simulated LLM hatası")
}

// cardErrChat, mercekleri hep "pass" geçirir ama kart üretim çağrısında
// hata döner (kart-üretimi aşamasındaki geçici hatayı simüle eder).
type cardErrChat struct{}

func (cardErrChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return cardErrChat{}.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (cardErrChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if strings.Contains(system, `"market_derived"`) {
		return "", fmt.Errorf("simulated kart üretimi hatası")
	}
	return `{"verdict":"pass","reason":"test-reason"}`, nil
}

func TestProcessSeedsLensErrorNoMarkThenRetries(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-lens-error"
	title := "Test Mercek Hata Fikri"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Lens Error Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	// 1. koşu: mercek çağrısı hata veriyor -> kart yok, mark da YAZILMAMALI.
	n1, err := ProcessSeeds(ctx, cfg, st, errChat{}, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (1. koşu): %v", err)
	}
	if n1 != 0 {
		t.Fatalf("0 idea beklenirdi, geldi: %d", n1)
	}
	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 0 {
		t.Errorf("LLM hatasında mark YAZILMAMALI (geçici hata tohumu kalıcı yakmamalı), geldi: %d", markCount)
	}

	// 2. koşu: LLM normal çalışıyor -> tohum yeniden denenmeli ve kart üretilmeli.
	chat := &fakeSeedChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	n2, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (2. koşu): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("yeniden denemede 1 idea beklenirdi, geldi: %d", n2)
	}
}

func TestProcessSeedsCardGenerationErrorNoMarkThenRetries(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-card-error"
	title := "Test Kart Hata Fikri"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Card Error Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	// 1. koşu: mercekler pass ama kart üretim çağrısı hata veriyor -> mark YAZILMAMALI.
	n1, err := ProcessSeeds(ctx, cfg, st, cardErrChat{}, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (1. koşu): %v", err)
	}
	if n1 != 0 {
		t.Fatalf("0 idea beklenirdi, geldi: %d", n1)
	}
	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 0 {
		t.Errorf("kart üretimi hatasında mark YAZILMAMALI, geldi: %d", markCount)
	}

	// 2. koşu: LLM normal çalışıyor -> tohum yeniden denenmeli ve kart üretilmeli.
	chat := &fakeSeedChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	n2, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (2. koşu): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("yeniden denemede 1 idea beklenirdi, geldi: %d", n2)
	}
}

func TestProcessSeedsDuplicateSkipsCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-dup"
	title := "Test Mukerrer Fikir XYZ"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Aynı başlık/problemle mevcut bir kart önceden var olsun: pg_trgm
	// benzerliği bire bir eşleşmede >= dupAutoThreshold olur, hakeme bile
	// gitmeden mükerrer sayılır.
	existing := store.Idea{
		Title: title, ProblemStatement: "aynı sorun tam metni", ProposedSolution: "x", TargetUser: "y",
		EvidenceCount: 3, SourceType: "pain_point", DomainTags: []string{"x"},
		UrgencyScore: 3, MonetizationSignal: 2,
	}
	if _, err := st.InsertIdea(ctx, existing); err != nil {
		t.Fatal(err)
	}

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Dup Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)

	chat := &fakeSeedChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"aynı sorun tam metni","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 0 {
		t.Fatalf("0 idea beklenirdi (mükerrer), geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 1 {
		t.Errorf("yalnızca önceden var olan kart olmalı (1), geldi: %d", ideaCount)
	}

	var evidenceCount int
	if err := st.Pool.QueryRow(ctx, "SELECT evidence_count FROM ideas WHERE title = $1", title).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 3 {
		t.Errorf("mükerrer tohumda MergeIdeaEvidence çağrılmamalı (evidence_count 3 sabit kalmalı), geldi: %d", evidenceCount)
	}
}

// TestSeedLensesExcludeDistinctiveness: özgünlük merceği ADVISORY olduğundan
// (#101 v3) bloklayıcı seedLenses/trendingLenses listelerinde YER ALMAMALI —
// kart üretildikten SONRA ayrıca çağrılır (bkz. distinctivenessAdvise).
func TestSeedLensesExcludeDistinctiveness(t *testing.T) {
	for _, l := range seedLenses {
		if l.system == lensDistinctivenessSystem {
			t.Error("seedLenses özgünlük merceğini İÇERMEMELİ (advisory, bloklamaz)")
		}
	}
	for _, l := range trendingLenses {
		if l.system == lensDistinctivenessSystem {
			t.Error("trendingLenses özgünlük merceğini İÇERMEMELİ (advisory, bloklamaz)")
		}
	}
}

// advisorySeedChat: 3 bloklayıcı mercek + kart üretimi + dedup normal
// davranır (pass/cardResponse/same:false); kart-sonrası ADVISORY özgünlük
// merceği (lensDistinctivenessSystem) ayrıca yapılandırılabilir bir cevap ya
// da hata döner — #101 v3'ün "kart her durumda yazılır" davranışını
// doğrulamak için.
type advisorySeedChat struct {
	cardResponse      string
	distinctVerdict   string // pass/fail/unsure; boşsa "pass"
	distinctCriterion string // boşsa "none"
	distinctErr       bool   // true ise lensDistinctivenessSystem hata döner

	// lastTemp, sıcaklık politikasını (#106) doğrulayan testler için son
	// çağrının sıcaklığını sistem prompt'una göre kaydeder.
	lastTemp map[string]float64
}

func (f *advisorySeedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *advisorySeedChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if f.lastTemp == nil {
		f.lastTemp = map[string]float64{}
	}
	f.lastTemp[system] = temp
	switch {
	case system == dupJudgeSystem:
		return `{"same": false}`, nil
	case strings.Contains(system, `"market_derived"`):
		return f.cardResponse, nil
	case system == lensDistinctivenessSystem:
		if f.distinctErr {
			return "", fmt.Errorf("simulated özgünlük merceği hatası")
		}
		v := f.distinctVerdict
		if v == "" {
			v = "pass"
		}
		c := f.distinctCriterion
		if c == "" {
			c = "none"
		}
		return fmt.Sprintf(`{"verdict":%q,"criterion":%q,"reason":"test-reason"}`, v, c), nil
	default:
		return `{"verdict":"pass","reason":"test-reason"}`, nil
	}
}

// TestProcessSeedsDistinctivenessFailStillWritesCard: özgünlük merceği
// "fail" (K1) dönse bile kart YAZILIR, alanlar karta doğru işlenir (#101 v3).
func TestProcessSeedsDistinctivenessFailStillWritesCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-obvious"
	title := "Test Bariz Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Obvious Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &advisorySeedChat{
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
		distinctVerdict:   "fail",
		distinctCriterion: "K1",
	}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 1 {
		t.Fatalf("özgünlük merceği fail dönse de kart yazılmalı (advisory), n=1 beklenirdi, geldi: %d", n)
	}

	var verdict, criterion, reason *string
	if err := st.Pool.QueryRow(ctx,
		"SELECT distinctiveness_verdict, distinctiveness_criterion, distinctiveness_reason FROM ideas WHERE title = $1", title).
		Scan(&verdict, &criterion, &reason); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if verdict == nil || *verdict != "fail" {
		t.Errorf("distinctiveness_verdict=fail beklenirdi, geldi: %v", verdict)
	}
	if criterion == nil || *criterion != "K1" {
		t.Errorf("distinctiveness_criterion=K1 beklenirdi, geldi: %v", criterion)
	}
	if reason == nil || *reason == "" {
		t.Error("distinctiveness_reason boş olmamalı")
	}
	if got := chat.lastTemp[lensDistinctivenessSystem]; got != 0 {
		t.Errorf("özgünlük merceği (#106) sıcaklık 0 ile çağrılmalı, geldi: %v", got)
	}
}

// TestProcessSeedsDistinctivenessErrorStillWritesCard: mercek çağrısı HATA
// verirse (ağ/kota) kart yine de yazılır, distinctiveness_* alanları NULL
// kalır (#101 v3 edge case).
func TestProcessSeedsDistinctivenessErrorStillWritesCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-distinct-err"
	title := "Test Ozgunluk Hata Tohum Fikri"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Distinct Err Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &advisorySeedChat{
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
		distinctErr: true,
	}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 1 {
		t.Fatalf("mercek HATA verse de kart yazılmalı, n=1 beklenirdi, geldi: %d", n)
	}

	var verdict, criterion, reason *string
	if err := st.Pool.QueryRow(ctx,
		"SELECT distinctiveness_verdict, distinctiveness_criterion, distinctiveness_reason FROM ideas WHERE title = $1", title).
		Scan(&verdict, &criterion, &reason); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if verdict != nil || criterion != nil || reason != nil {
		t.Errorf("mercek hatasında distinctiveness_* alanları NULL kalmalı, geldi: verdict=%v criterion=%v reason=%v", verdict, criterion, reason)
	}
}

// sp, testte *string literal üretmek için kısa yardımcı.
func sp(s string) *string { return &s }

// TestDistinctivenessLogSuffix, kart üretim log satırına eklenen özgünlük
// özetini doğrular (#108): fail -> kriter kodu + TR açıklaması + sebep (ilk
// 160 karakter), pass/unsure -> yalnız karar, alanlar NULL -> boş string.
func TestDistinctivenessLogSuffix(t *testing.T) {
	longReason := strings.Repeat("a", 200)
	cases := []struct {
		name string
		idea store.Idea
		want string
	}{
		{
			name: "fail K3 bilinen kriter",
			idea: store.Idea{
				DistinctivenessVerdict:   sp("fail"),
				DistinctivenessCriterion: sp("K3"),
				DistinctivenessReason:    sp("TR'de ödeyen segment yok"),
			},
			want: " · özgünlük: fail K3 — talep (TR'de ödeyen yok) — TR'de ödeyen segment yok",
		},
		{
			name: "pass",
			idea: store.Idea{
				DistinctivenessVerdict:   sp("pass"),
				DistinctivenessCriterion: sp("none"),
				DistinctivenessReason:    sp("her şey yolunda"),
			},
			want: " · özgünlük: pass",
		},
		{
			name: "unsure",
			idea: store.Idea{
				DistinctivenessVerdict: sp("unsure"),
			},
			want: " · özgünlük: unsure",
		},
		{
			name: "mercek hiç çalışmadı (alanlar NULL)",
			idea: store.Idea{},
			want: "",
		},
		{
			name: "fail sebep 160 karaktere kırpılır",
			idea: store.Idea{
				DistinctivenessVerdict:   sp("fail"),
				DistinctivenessCriterion: sp("K1"),
				DistinctivenessReason:    sp(longReason),
			},
			want: " · özgünlük: fail K1 — doygunluk (10+ bilinir benzer ürün) — " + clip(longReason, 160),
		},
		{
			// "a" + çok baytlı Türkçe karakterler (ğ, 2 bayt/rune): 160. BAYT
			// tam bir "ğ"nin ortasına denk gelir (1 + 2k tek, 160 çift) —
			// byte-bazlı kırpma bu durumda geçersiz UTF-8 üretirdi. Rune-bazlı
			// clip() bunu doğru kırpar (#108 reviewer bulgusu).
			name: "fail sebep çok baytlı Türkçe karakterle 160 rune'a kırpılır (geçerli UTF-8)",
			idea: store.Idea{
				DistinctivenessVerdict:   sp("fail"),
				DistinctivenessCriterion: sp("K4"),
				DistinctivenessReason:    sp("a" + strings.Repeat("ğ", 200)),
			},
			want: " · özgünlük: fail K4 — kırılganlık (tek güncellemeyle anlamsız) — " + clip("a"+strings.Repeat("ğ", 200), 160),
		},
		{
			name: "fail K5 heyecan (#114)",
			idea: store.Idea{
				DistinctivenessVerdict:   sp("fail"),
				DistinctivenessCriterion: sp("K5"),
				DistinctivenessReason:    sp("bilinen ürünün TR'ye taşınmış hali"),
			},
			want: " · özgünlük: fail K5 — heyecan (bilinen mekaniğin başka pazara taşınmış hali) — bilinen ürünün TR'ye taşınmış hali",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := distinctivenessLogSuffix(tc.idea)
			if got != tc.want {
				t.Errorf("distinctivenessLogSuffix() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("distinctivenessLogSuffix() geçersiz UTF-8 ürettü: %q", got)
			}
		})
	}
}
