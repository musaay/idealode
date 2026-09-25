package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/store"
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
// göre sabit cevap döner: 2 merceğe lensVerdict, kart üretimine
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

// mustLensVerdicts, başlığa göre bir kartın lens_verdicts jsonb kolonunu
// okur (#164). GetIdea/GetIdeaBySlug KULLANILAMAZ — ikisi de
// published_at IS NOT NULL filtreler, test kartları henüz yayında değil
// (moderasyon kuyruğunda); ham SQL doğrudan okur.
func mustLensVerdicts(t *testing.T, ctx context.Context, st *store.Store, title string) []store.LensVerdict {
	t.Helper()
	var lv []store.LensVerdict
	if err := st.Pool.QueryRow(ctx, "SELECT lens_verdicts FROM ideas WHERE title = $1", title).Scan(&lv); err != nil {
		t.Fatalf("lens_verdicts okunamadı (%q): %v", title, err)
	}
	return lv
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

	// #164, #169: lens_verdicts kalıcı kaydı — 2 bloklayıcı mercek subject="seed"
	// (ham tohum alanları üzerinde çalıştı) + özgünlük subject="card" (kart
	// üretildikten SONRA çalıştı), hepsi verdict="pass".
	lensVerdicts := mustLensVerdicts(t, ctx, st, title)
	if len(lensVerdicts) != 3 {
		t.Fatalf("lens_verdicts 3 eleman beklenirdi (2 bloklayıcı + özgünlük), geldi %d: %+v", len(lensVerdicts), lensVerdicts)
	}
	for i, lv := range lensVerdicts[:2] {
		if lv.Subject != "seed" {
			t.Errorf("lens_verdicts[%d].Subject=seed beklenirdi (bloklayıcı mercekler), geldi %q", i, lv.Subject)
		}
		if lv.Verdict != "pass" {
			t.Errorf("lens_verdicts[%d].Verdict=pass beklenirdi, geldi %q", i, lv.Verdict)
		}
		if lv.PromptVersion != "v1" {
			t.Errorf("lens_verdicts[%d].PromptVersion=v1 beklenirdi, geldi %q", i, lv.PromptVersion)
		}
		if lv.Lens == "pazar-işlerliği" {
			t.Errorf("lens_verdicts[%d] pazar-işlerliği İÇERMEMELİ (#169), geldi: %+v", i, lv)
		}
	}
	last := lensVerdicts[2]
	if last.Lens != "özgünlük" || last.Subject != "card" || last.Verdict != "pass" {
		t.Errorf("lens_verdicts[2] özgünlük/card/pass beklenirdi, geldi: %+v", last)
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

	// Sıcaklık politikası (#106): 2 bloklayıcı mercek yargı=0, kart üretimi
	// (LLM sistem prompt'unda "market_derived" geçer) üretim=0.3.
	if len(chat.lastTemp) < 4 {
		t.Fatalf("beklenen çağrı sayısına ulaşılmadı (2 mercek + özgünlük + kart üretimi): %d", len(chat.lastTemp))
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
	seedName := "Fail Seed"
	title := "Test Elenen Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", seedName)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":%q,"summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedName, seedURL)

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

	// #138: mercek elemesi eliminations'a da düşmeli — kart henüz
	// üretilmediğinden (mercekler tohum üzerinde çalıştı) detail seed
	// özeti olmalı, subject tohum adı.
	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "blocking_lens" && elims[i].Subject == seedName {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("mercek elemesi eliminations'a stage=blocking_lens kaydı düşürmeli")
	}
	if found.Detail == nil || *found.Detail != "özet" {
		t.Errorf("eliminations.detail tohumun özeti olmalı (%q), geldi: %v", "özet", found.Detail)
	}
	// #164, #169: 2 mercek de "fail" (fakeSeedChat hepsine fail döner, tohum
	// yolunda stopOnFirstFail=false → TÜMÜ çağrılır) — check isimlerin ", "
	// ile birleşimi (mevcut log biçimiyle birebir), verdicts 2 eleman,
	// hepsi subject=seed verdict=fail.
	if found.Check == nil {
		t.Fatal("eliminations.check dolu olmalı (bloklayan mercek adları)")
	}
	for _, name := range []string{seedLenses[0].name, seedLenses[1].name} {
		if !strings.Contains(*found.Check, name) {
			t.Errorf("eliminations.check %q içermeli, geldi: %q", name, *found.Check)
		}
	}
	if len(found.Verdicts) != 2 {
		t.Fatalf("eliminations.verdicts 2 eleman beklenirdi (2 bloklayıcı mercek), geldi %d: %+v", len(found.Verdicts), found.Verdicts)
	}
	for i, lv := range found.Verdicts {
		if lv.Subject != "seed" || lv.Verdict != "fail" {
			t.Errorf("verdicts[%d] seed/fail beklenirdi, geldi: %+v", i, lv)
		}
	}
}

// TestProcessSeedsUnsureCreatesCardAndMarksOnce: #131 PO düzeltmesi —
// mercek(ler) "unsure" dönerse (hiç "fail" yoksa) tohum ARTIK BLOKLANMAZ:
// kart normal üretilir (organik yoldaki blockedByIdeaLens ile AYNI ilke).
// İlk tasarım (unsure'u da LLM-hatası gibi "yeniden dene, imleçsiz atla"
// sayan) yanlıştı: mercek çağrıları sıcaklık 0 olduğundan aynı tohum+prompt
// HER KOŞUDA aynı "unsure" cevabını verir — hata GEÇİCİDİR, unsure
// DETERMİNİSTİKTİR; tohum hiç ilerlemez, koşu başına mercek bütçesini
// sonsuza dek boşa yakardı. Bu test: (a) kart üretildiğini, (b) veri-erişimi
// merceğinin ham "unsure" kararının karta yazıldığını, (c) markProcessed'in
// kart-yazımının kendi imleç adımıyla (ayrı bir "unsure" dalı OLMADAN, çift
// yazım yok) TAM BİR KEZ çalıştığını, (d) tohumun bu yüzden bir daha
// işlenmediğini doğrular.
func TestProcessSeedsUnsureCreatesCardAndMarksOnce(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-unsure"
	title := "Test Belirsiz Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Unsure Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	// 2 mercek de "unsure" -> kart YİNE DE üretilmeli (bloklamıyor).
	chat := &fakeSeedChat{
		lensVerdict: "unsure",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
	}
	n1, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (1. koşu): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("unsure bloklamamalı, 1 idea beklenirdi, geldi: %d", n1)
	}

	var ideaCount int
	var dataAccessVerdict, dataAccessReason *string
	if err := st.Pool.QueryRow(ctx,
		"SELECT data_access_verdict, data_access_reason FROM ideas WHERE title = $1", title).
		Scan(&dataAccessVerdict, &dataAccessReason); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 1 {
		t.Errorf("unsure tohumdan tam olarak 1 kart üretilmeli, geldi: %d", ideaCount)
	}
	if dataAccessVerdict == nil || *dataAccessVerdict != "unsure" {
		t.Errorf("data_access_verdict=unsure beklenirdi, geldi: %v", dataAccessVerdict)
	}
	if dataAccessReason == nil || *dataAccessReason != "test-reason" {
		t.Errorf("data_access_reason=%q beklenirdi, geldi: %v", "test-reason", dataAccessReason)
	}

	// markProcessed AYRI bir "unsure" dalından değil, kart yazımının kendi
	// imleç adımından (InsertIdea sonrası) çalışır — tam bir kez, çift
	// yazım yok (InsertRawPosts zaten ON CONFLICT DO NOTHING ama burada
	// asıl doğrulanan: raw_posts satırı var VE tohum bir daha işlenmiyor).
	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Errorf("kart üretildiğinde imleç TAM BİR KEZ yazılmalı, geldi: %d", markCount)
	}

	// 2. koşu: tohum ZATEN işlenmiş (imleç var) -> yeniden denenMEMELİ.
	n2, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds (2. koşu): %v", err)
	}
	if n2 != 0 {
		t.Errorf("kart üretilmiş tohum ikinci koşuda tekrar işlenmemeli, geldi: %d", n2)
	}
}

// perLensSeedChat, MERCEK BAŞINA farklı verdict dönebilen sahte chat —
// fakeSeedChat'in aksine tüm bloklayıcı mercekleri TEK bir verdict'e
// zorlamaz, lens.system'e göre haritalar; haritada olmayan sistem prompt'u
// (kart üretimi dahil) "pass" döner. "fail baskındır" karışık senaryosunu
// (#131) sınamak için.
type perLensSeedChat struct {
	verdicts     map[string]string // lens.system -> verdict
	cardResponse string
}

func (f *perLensSeedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *perLensSeedChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if strings.Contains(system, `"market_derived"`) {
		return f.cardResponse, nil
	}
	if v, ok := f.verdicts[system]; ok {
		return fmt.Sprintf(`{"verdict":%q,"reason":"test-reason"}`, v), nil
	}
	return `{"verdict":"pass","reason":"test-reason"}`, nil
}

// TestProcessSeedsFailDominatesOverUnsureMarksProcessed: #131 edge case —
// 2 mercekten biri "fail" biri "unsure" dönerse fail baskındır: tohum
// elenmiş SAYILIR ve markProcessed ÇAĞRILIR (unsure'un "kalıcı yakma YOK"
// istisnası burada geçerli DEĞİL, çünkü aynı tohumda ayrıca bir "fail" var).
func TestProcessSeedsFailDominatesOverUnsureMarksProcessed(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-fail-dominant"
	seedName := "Fail Dominant Seed"
	title := "Test Fail Baskin Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
		// data-access merceği fail baskın geldiğinde stage=blocking_lens
		// yazar (#138) — silinmezse kalıcı satır store paketindeki sayım
		// testini (EliminationCountsSince, 1 dakikalık pencere) kirletir.
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", seedName)
	}
	cleanup()
	t.Cleanup(cleanup)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":%q,"summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedName, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &perLensSeedChat{
		verdicts: map[string]string{
			lensThirdPartySystem: "unsure",
			lensDataAccessSystem: "fail",
		},
	}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 0 {
		t.Fatalf("0 idea beklenirdi (fail eledi), geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Errorf("fail+unsure karışımında kart üretilmemeli, geldi: %d", ideaCount)
	}

	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Errorf("fail baskın olduğundan mark YAZILMALI (tohum işlenmiş sayılır), geldi: %d", markCount)
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

// TestSeedLensesExcludeDistinctiveness: özgünlük merceği (#101 v3) toplu
// bloklayıcı seedLenses/trendingLenses listelerinde YER ALMAMALI — kart
// üretildikten SONRA AYRICA çağrılır (bkz. distinctivenessCheck); #138/#166
// ile K1|K2 bloklayıcı olsa da bu ayrı-çağrılma düzeni değişmedi.
func TestSeedLensesExcludeDistinctiveness(t *testing.T) {
	for _, l := range seedLenses {
		if l.system == lensDistinctivenessSystem {
			t.Error("seedLenses özgünlük merceğini İÇERMEMELİ (ayrı çağrılır)")
		}
	}
	for _, l := range trendingLenses {
		if l.system == lensDistinctivenessSystem {
			t.Error("trendingLenses özgünlük merceğini İÇERMEMELİ (ayrı çağrılır)")
		}
	}
}

// distinctSeedChat: 2 bloklayıcı mercek + kart üretimi + dedup normal
// davranır (pass/cardResponse/same:false); kart-sonrası özgünlük
// merceği (lensDistinctivenessSystem) ayrıca yapılandırılabilir bir cevap ya
// da hata döner — #101 v3/#138/#166'nın "K1|K2 dışında kart her durumda
// yazılır, K1|K2 fail'i bloklar" davranışını doğrulamak için.
type distinctSeedChat struct {
	cardResponse      string
	distinctVerdict   string // pass/fail/unsure; boşsa "pass"
	distinctCriterion string // boşsa "none"
	distinctErr       bool   // true ise lensDistinctivenessSystem hata döner

	// lastTemp, sıcaklık politikasını (#106) doğrulayan testler için son
	// çağrının sıcaklığını sistem prompt'una göre kaydeder.
	lastTemp map[string]float64
}

func (f *distinctSeedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *distinctSeedChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
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
// "fail" (K3 — henüz bloklamayan kriterlerden biri, #138) dönse bile kart
// YAZILIR, alanlar karta doğru işlenir VE eliminations'a stage=distinctiveness
// bir satır düşer (kart yazılmasa da yazılsa da "fail" kaydedilir, #138).
func TestProcessSeedsDistinctivenessFailStillWritesCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-obvious"
	title := "Test Bariz Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Obvious Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &distinctSeedChat{
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
		distinctVerdict:   "fail",
		distinctCriterion: "K3",
	}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 1 {
		t.Fatalf("K3 fail bloklamamalı, kart yazılmalı, n=1 beklenirdi, geldi: %d", n)
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
	if criterion == nil || *criterion != "K3" {
		t.Errorf("distinctiveness_criterion=K3 beklenirdi, geldi: %v", criterion)
	}
	if reason == nil || *reason == "" {
		t.Error("distinctiveness_reason boş olmamalı")
	}
	if got := chat.lastTemp[lensDistinctivenessSystem]; got != 0 {
		t.Errorf("özgünlük merceği (#106) sıcaklık 0 ile çağrılmalı, geldi: %v", got)
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "distinctiveness" && elims[i].Subject == title {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("K3 fail eliminations'a stage=distinctiveness kaydı düşürmeli")
	}
	if found.Criterion == nil || *found.Criterion != "K3" {
		t.Errorf("eliminations.criterion=K3 beklenirdi, geldi: %v", found.Criterion)
	}
	if found.Detail == nil || *found.Detail != "sorun" {
		t.Errorf("eliminations.detail kartın problem_statement'ı olmalı (%q), geldi: %v", "sorun", found.Detail)
	}
}

// TestProcessSeedsDistinctivenessK1BlocksCard: özgünlük merceği "fail" K1
// (doygunluk) dönerse kart DB'ye YAZILMAZ, tohum yine de mark'lanır (bir
// daha denenmez — deterministik sonuç, #131'deki hasFail ile AYNI ilke) ve
// eliminations'a stage=distinctiveness bir satır düşer (#138).
func TestProcessSeedsDistinctivenessK1BlocksCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-saturated"
	title := "Test Doygun Fikir"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Saturated Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &distinctSeedChat{
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
	if n != 0 {
		t.Fatalf("K1 fail bloklamalı, kart YAZILMAMALI, n=0 beklenirdi, geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Error("K1 ile bloklanan kart DB'ye yazılmamalı")
	}

	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Error("K1 ile bloklanan tohum da mark'lanmalı (yeniden işlenmesin, LLM maliyeti tekrarlanmasın)")
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "distinctiveness" && elims[i].Subject == title {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("K1 bloğu eliminations'a stage=distinctiveness kaydı düşürmeli")
	}
	if found.Criterion == nil || *found.Criterion != "K1" {
		t.Errorf("eliminations.criterion=K1 beklenirdi, geldi: %v", found.Criterion)
	}
	if found.Detail == nil || *found.Detail != "sorun" {
		t.Errorf("eliminations.detail kartın problem_statement'ı olmalı (%q), geldi: %v", "sorun", found.Detail)
	}
	// #164, #169: kart hiç yazılmadığından (K1 bloğu) mercek kararlarının TEK
	// kalıcı yeri eliminations.check/verdicts — check=özgünlük (bloklayan
	// mercek), verdicts 2 bloklayıcı mercek (subject=seed, pass) + özgünlük
	// (subject=card, fail) TÜMÜNÜ taşır.
	if found.Check == nil || *found.Check != "özgünlük" {
		t.Errorf("eliminations.check=özgünlük beklenirdi, geldi: %v", found.Check)
	}
	if len(found.Verdicts) != 3 {
		t.Fatalf("eliminations.verdicts 3 eleman beklenirdi (2 bloklayıcı + özgünlük), geldi %d: %+v", len(found.Verdicts), found.Verdicts)
	}
	for i, lv := range found.Verdicts[:2] {
		if lv.Subject != "seed" || lv.Verdict != "pass" {
			t.Errorf("verdicts[%d] seed/pass beklenirdi, geldi: %+v", i, lv)
		}
	}
	if last := found.Verdicts[2]; last.Lens != "özgünlük" || last.Subject != "card" || last.Verdict != "fail" {
		t.Errorf("verdicts[2] özgünlük/card/fail beklenirdi, geldi: %+v", last)
	}
}

// TestProcessSeedsDistinctivenessK2BlocksCard: özgünlük merceği "fail" K2
// (yerleşik çözüm) dönerse de K1 gibi kart DB'ye YAZILMAZ, tohum yine de
// mark'lanır ve eliminations'a stage=distinctiveness criterion=K2 bir satır
// düşer (#166: K2 artık K1 ile AYNI şekilde bloklayıcı).
func TestProcessSeedsDistinctivenessK2BlocksCard(t *testing.T) {
	st := seedTestStore(t)
	ctx := context.Background()

	seedURL := "https://example.com/seed-native"
	title := "Test Yerleşik Çözüm Fikri"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":"Native Seed","summary":"özet","evidence":"kanıt","source_url":%q,"tr_angle":"TR açısı"}`, seedURL)
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	chat := &distinctSeedChat{
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":4,"monetization_signal":4,
			"known_competitors_ai_guess":"","domain_tags":["test-seed-tag"]}`, title),
		distinctVerdict:   "fail",
		distinctCriterion: "K2",
	}

	n, err := ProcessSeeds(ctx, cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 0 {
		t.Fatalf("K2 fail bloklamalı, kart YAZILMAMALI, n=0 beklenirdi, geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Error("K2 ile bloklanan kart DB'ye yazılmamalı")
	}

	var markCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).
		Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Error("K2 ile bloklanan tohum da mark'lanmalı (yeniden işlenmesin, LLM maliyeti tekrarlanmasın)")
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "distinctiveness" && elims[i].Subject == title {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("K2 bloğu eliminations'a stage=distinctiveness kaydı düşürmeli")
	}
	if found.Criterion == nil || *found.Criterion != "K2" {
		t.Errorf("eliminations.criterion=K2 beklenirdi, geldi: %v", found.Criterion)
	}
	if found.Detail == nil || *found.Detail != "sorun" {
		t.Errorf("eliminations.detail kartın problem_statement'ı olmalı (%q), geldi: %v", "sorun", found.Detail)
	}
	if len(found.Verdicts) != 3 {
		t.Fatalf("eliminations.verdicts 3 eleman beklenirdi (2 bloklayıcı + özgünlük), geldi %d: %+v", len(found.Verdicts), found.Verdicts)
	}
	if last := found.Verdicts[2]; last.Lens != "özgünlük" || last.Subject != "card" || last.Verdict != "fail" {
		t.Errorf("verdicts[2] özgünlük/card/fail beklenirdi, geldi: %+v", last)
	}
	// prompt_version #164/#166: distinctiveness merceğinin v4 metnine
	// geçtiği kalıcı kayda ("v4") yansımalı.
	if last := found.Verdicts[2]; last.PromptVersion != "v4" {
		t.Errorf("verdicts[2].PromptVersion=v4 beklenirdi, geldi: %q", last.PromptVersion)
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

	chat := &distinctSeedChat{
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
