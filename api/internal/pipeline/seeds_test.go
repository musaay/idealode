package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

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

// fakeSeedChat, ProcessSeeds entegrasyon testleri için sistem prompt'una
// göre sabit cevap döner: 3 merceğe lensVerdict, kart üretimine
// cardResponse, dedup hakemine (gerekirse) dupSame.
type fakeSeedChat struct {
	lensVerdict  string
	cardResponse string
	dupSame      bool
}

func (f *fakeSeedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
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

// cardErrChat, mercekleri hep "pass" geçirir ama kart üretim çağrısında
// hata döner (kart-üretimi aşamasındaki geçici hatayı simüle eder).
type cardErrChat struct{}

func (cardErrChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
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
