package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/store"
)

func TestParseIdeaResponseClamps(t *testing.T) {
	raw := `{"title":" Fatura Robotu ","problem_statement":"p","proposed_solution":"s",
		"target_user":"t","example_quotes":["q1","q2","q3","q4","q5","q6"],
		"urgency_score":9,"monetization_signal":-2,
		"known_competitors_ai_guess":"X (AI tahmini)","domain_tags":["Invoice Automation"]}`

	idea, err := parseIdeaResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if idea.Title != "Fatura Robotu" {
		t.Errorf("title trim edilmeli: %q", idea.Title)
	}
	if idea.UrgencyScore != 5 || idea.MonetizationSignal != 0 {
		t.Errorf("skorlar kıstırılmalı (5, 0): %d, %d", idea.UrgencyScore, idea.MonetizationSignal)
	}
	if len(idea.ExampleQuotes) != 5 {
		t.Errorf("alıntılar 5 ile sınırlanmalı: %d", len(idea.ExampleQuotes))
	}
	if idea.DomainTags[0] != "invoice-automation" {
		t.Errorf("tag slug'lanmalı: %v", idea.DomainTags)
	}
}

func TestParseIdeaResponseSkip(t *testing.T) {
	_, err := parseIdeaResponse(`{"skip": true, "reason": "vendor-internal"}`)
	if !errors.Is(err, errVendorInternal) {
		t.Errorf("skip cevabı errVendorInternal dönmeli, geldi: %v", err)
	}
	// skip:false normal akışı bozmamalı
	if _, err := parseIdeaResponse(`{"skip": false, "title": "X", "problem_statement": "p"}`); errors.Is(err, errVendorInternal) {
		t.Error("skip:false vendor-internal sayılmamalı")
	}
	// reason belirsiz/boşsa güvenli varsayılan: vendor-internal (mevcut davranış korunur)
	if _, err := parseIdeaResponse(`{"skip": true}`); !errors.Is(err, errVendorInternal) {
		t.Errorf("reason'sız skip errVendorInternal'a düşmeli, geldi: %v", err)
	}
}

// TestParseIdeaResponseDataLockedIsSeparateFromVendorInternal (#134):
// reason="data-locked" artık AYRI bir hataya (errDataLocked) düşer —
// errVendorInternal ile aynı kovaya girmemeli, çünkü SynthesizeIdeas ikisini
// farklı işler (data-locked temayı kalıcı gömmez).
func TestParseIdeaResponseDataLockedIsSeparateFromVendorInternal(t *testing.T) {
	_, err := parseIdeaResponse(`{"skip": true, "reason": "data-locked"}`)
	if !errors.Is(err, errDataLocked) {
		t.Errorf("reason=data-locked errDataLocked dönmeli, geldi: %v", err)
	}
	if errors.Is(err, errVendorInternal) {
		t.Error("data-locked errVendorInternal ile AYNI hata olmamalı (#134)")
	}
}

func TestParseIdeaResponseRejectsEmpty(t *testing.T) {
	if _, err := parseIdeaResponse(`{"title":"  "}`); err == nil {
		t.Error("boş title hata dönmeli")
	}
	if _, err := parseIdeaResponse(`garbage`); err == nil {
		t.Error("JSON olmayan yanıt hata dönmeli")
	}
}

// TestSynthesizeOneUsesDefaultTemperature, üretim çağrısının (kart metni)
// sabit 0.3 sıcaklıkla gittiğini doğrular (#106 — değişmez).
func TestSynthesizeOneUsesDefaultTemperature(t *testing.T) {
	chat := &fakeChat{response: `{"title":"Başlık Yeterince Uzun","problem_statement":"sorun",
		"proposed_solution":"çözüm","target_user":"kullanıcı","domain_tags":["x"]}`}
	cfg := &config.Config{OutputLang: "tr"}
	th := store.Theme{ID: 1, Name: "test-tag", Frequency: 3}
	evidence := []store.RawPost{{Title: "a", Body: "b"}}

	if _, err := synthesizeOne(context.Background(), cfg, chat, th, evidence); err != nil {
		t.Fatalf("synthesizeOne: %v", err)
	}
	found := false
	for _, temp := range chat.lastTemp {
		found = true
		if temp != 0.3 {
			t.Errorf("üretim çağrısı sıcaklık 0.3 olmalı, geldi: %v", temp)
		}
	}
	if !found {
		t.Fatal("chat hiç çağrılmamış")
	}
}

// TestDistinctivenessCheckUsesTemperatureZero, özgünlük merceğinin (yargı
// çağrısı, #106) sıcaklık 0 ile çağrıldığını doğrular.
func TestDistinctivenessCheckUsesTemperatureZero(t *testing.T) {
	chat := &fakeChat{}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}
	if err := distinctivenessCheck(context.Background(), chat, idea); err != nil {
		t.Fatalf("distinctivenessCheck: %v", err)
	}
	if got := chat.lastTemp[lensDistinctivenessSystem]; got != 0 {
		t.Errorf("özgünlük merceği sıcaklık 0 ile çağrılmalı, geldi: %v", got)
	}
}

// fakeChat, synthesize entegrasyon testi için sabit yanıt döner; tutarlılık
// denetimi çağrısına ise tüm post'ları tutarlı sayan bir cevap verir.
// distinctVerdict/distinctCriterion, kart-sonrası özgünlük
// merceğinin (#101 v3) döneceği cevabı belirler — distinctVerdict boşsa
// (zero value) "pass" varsayılır, mevcut mutlu yol testleri değişmeden
// geçsin diye.
type fakeChat struct {
	response          string
	distinctVerdict   string
	distinctCriterion string

	// lastTemp, sıcaklık politikasını (#106) doğrulayan testler için son
	// çağrının sıcaklığını sistem prompt'una göre kaydeder.
	lastTemp map[string]float64
}

func (f *fakeChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *fakeChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if f.lastTemp == nil {
		f.lastTemp = map[string]float64{}
	}
	f.lastTemp[system] = temp
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
	}
	if system == dupJudgeSystem {
		return `{"same": false}`, nil
	}
	if system == lensDistinctivenessSystem {
		v := f.distinctVerdict
		if v == "" {
			v = "pass"
		}
		c := f.distinctCriterion
		if c == "" {
			c = "none"
		}
		return fmt.Sprintf(`{"verdict":%q,"criterion":%q,"reason":"test-reason"}`, v, c), nil
	}
	return f.response, nil
}

func TestCoherentSubsetFiltersInvalidIndices(t *testing.T) {
	evidence := []store.RawPost{
		{Title: "a"}, {Title: "b"}, {Title: "c"},
	}
	chat := &indicesChat{response: `{"indices":[2,0,2,7,-1]}`}
	subset, err := coherentSubset(context.Background(), chat, evidence)
	if err != nil {
		t.Fatalf("coherentSubset: %v", err)
	}
	// Geçersiz (7, -1) ve tekrar eden (2) indeksler elenir.
	if len(subset) != 2 || subset[0].Title != "c" || subset[1].Title != "a" {
		t.Errorf("beklenen [c a], geldi: %+v", subset)
	}
}

// TestCoherentSubsetUsesTemperatureZero, yargı çağrısının (#106) sıcaklık
// 0 ile gittiğini doğrular.
func TestCoherentSubsetUsesTemperatureZero(t *testing.T) {
	chat := &indicesChat{response: `{"indices":[0]}`}
	if _, err := coherentSubset(context.Background(), chat, []store.RawPost{{Title: "a"}}); err != nil {
		t.Fatalf("coherentSubset: %v", err)
	}
	if chat.lastTemp != 0 {
		t.Errorf("coherentSubset sıcaklık 0 ile çağırmalı, geldi: %v", chat.lastTemp)
	}
}

func TestCoherentSubsetRejectsGarbage(t *testing.T) {
	chat := &indicesChat{response: `garbage`}
	if _, err := coherentSubset(context.Background(), chat, []store.RawPost{{Title: "a"}}); err == nil {
		t.Error("JSON olmayan tutarlılık cevabı hata dönmeli")
	}
}

func TestCoherenceIndicesFlexibleFormats(t *testing.T) {
	cases := []struct {
		raw  string
		n    int
		want []int
	}{
		{`{"indices":[0,1,3]}`, 8, []int{0, 1, 3}},       // düzgün format
		{`{"indices":["013"]}`, 8, []int{0, 1, 3}},       // bitişik string (gpt-oss'un döndüğü)
		{`{"indices":["0,2,5"]}`, 8, []int{0, 2, 5}},     // virgüllü string
		{`{"indices":["2"]}`, 8, []int{2}},               // sayı-string
		{`{"indices":[13567]}`, 8, []int{1, 3, 5, 6, 7}}, // bitişik SAYI (gpt-oss'un döndüğü)
		{`{"indices":[7,9,-1,7]}`, 8, []int{7}},          // negatif + tekrar elenir; 9 tek hane, atlanır
		{`{"indices":[]}`, 8, nil},                       // boş
	}
	for _, c := range cases {
		got, err := coherenceIndices(c.raw, c.n)
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: beklenen %v, geldi %v", c.raw, c.want, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: beklenen %v, geldi %v", c.raw, c.want, got)
				break
			}
		}
	}
}

// indicesChat, tutarlılık denetimi birim testleri için sabit cevap döner.
type indicesChat struct {
	response string
	lastTemp float64 // son çağrının sıcaklığı (#106 doğrulaması için)
}

func (c *indicesChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *indicesChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.lastTemp = temp
	return c.response, nil
}

func TestSynthesizeIdeasIntegration(t *testing.T) {
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

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = 'Test Sentez Fikri'")
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-syn'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = 'test-syn-tag'")
	}
	cleanup()
	t.Cleanup(cleanup)

	// 3 post + analiz + tema kur
	posts := []store.RawPost{
		{Platform: "test-syn", SourceRef: "s1", Community: "c", Title: "I wish X", Body: "quote one"},
		{Platform: "test-syn", SourceRef: "s2", Community: "c", Title: "I wish Y", Body: "quote two"},
		{Platform: "test-syn", SourceRef: "s3", Community: "c", Title: "I wish Z", Body: "quote three"},
	}
	if _, err := st.InsertRawPosts(ctx, posts); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Pool.Query(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-syn'")
	var analyses []store.PostAnalysis
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		analyses = append(analyses, store.PostAnalysis{
			PostID: id, Classification: "pain_point", DomainTags: []string{"test-syn-tag"},
		})
	}
	rows.Close()
	if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupThemes(ctx, st, themeFallbackChat{}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &fakeChat{response: `{"title":"Test Sentez Fikri","problem_statement":"sorun",
		"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
		"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":["test-syn-tag"]}`}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 1 {
		t.Fatalf("1 idea beklendi, geldi: %d", n)
	}

	var evidenceCount int
	var sourceType string
	if err := st.Pool.QueryRow(ctx,
		"SELECT evidence_count, source_type FROM ideas WHERE title = 'Test Sentez Fikri'").
		Scan(&evidenceCount, &sourceType); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if evidenceCount != 3 || sourceType != "pain_point" {
		t.Errorf("evidence_count=3, source_type=pain_point beklenirdi: %d, %s", evidenceCount, sourceType)
	}

	// Aynı tema ikinci koşuda tekrar idea üretmemeli (tema bazlı dedup)
	n2, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas (2. koşu): %v", err)
	}
	if n2 != 0 {
		t.Errorf("ikinci koşu 0 idea üretmeli, geldi: %d", n2)
	}
}

// synthDistinctErrChat: coherence/dup/kart üretimi normal davranır, ama
// kart-sonrası özgünlük merceği (lensDistinctivenessSystem)
// çağrısında hata verir — ağ/kota kesintisi simülasyonu (#101 v3: bloklama
// YOK, kart yine de yazılır; yalnız distinctiveness_* alanları NULL kalır).
type synthDistinctErrChat struct{ response string }

func (f *synthDistinctErrChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *synthDistinctErrChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
	}
	if system == lensDistinctivenessSystem {
		return "", fmt.Errorf("simulated özgünlük merceği hatası")
	}
	if system == dupJudgeSystem {
		return `{"same": false}`, nil
	}
	return f.response, nil
}

// setupSynthTheme, DB'de bir tema kurar (post + analiz + tema gruplama) —
// distinctiveness testlerinin ortak fikstürü. willing, üç post_analysis
// satırının willingness_to_pay değerini belirler (#121/#125 ödeme sinyali
// testleri için).
func setupSynthTheme(t *testing.T, ctx context.Context, st *store.Store, platform, tag string, willing bool) {
	t.Helper()
	posts := []store.RawPost{
		{Platform: platform, SourceRef: "s1", Community: "c", Title: "I wish X", Body: "quote one"},
		{Platform: platform, SourceRef: "s2", Community: "c", Title: "I wish Y", Body: "quote two"},
		{Platform: platform, SourceRef: "s3", Community: "c", Title: "I wish Z", Body: "quote three"},
	}
	if _, err := st.InsertRawPosts(ctx, posts); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Pool.Query(ctx, "SELECT id FROM raw_posts WHERE platform = $1", platform)
	var analyses []store.PostAnalysis
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		analyses = append(analyses, store.PostAnalysis{
			PostID: id, Classification: "pain_point", DomainTags: []string{tag},
			WillingnessToPay: willing,
		})
	}
	rows.Close()
	if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupThemes(ctx, st, themeFallbackChat{}); err != nil {
		t.Fatal(err)
	}
}

// TestSynthesizeIdeasDistinctivenessFailStillWritesCard: özgünlük merceği
// "fail" (K3 — henüz bloklamayan kriterlerden biri, #138) dönse bile kart
// YAZILIR — tema damgalanmaz (MarkThemeIncoherent bu yüzden çağrılmaz),
// alanlar karta doğru yazılır VE eliminations'a stage=distinctiveness bir
// satır düşer (kart bloklanmasa da "fail" kaydedilir, #138).
func TestSynthesizeIdeasDistinctivenessFailStillWritesCard(t *testing.T) {
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

	title := "Test Bariz Sentez Fikri"
	platform, tag := "test-syn-distinct-fail", "test-syn-distinct-fail-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &fakeChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		distinctVerdict:   "fail",
		distinctCriterion: "K3",
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
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

	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("K3 fail (bloklamayan kriter) MarkThemeIncoherent çağırmamalı")
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

// TestSynthesizeIdeasDistinctivenessK1BlocksAndRecords: özgünlük merceği
// "fail" K1 (doygunluk) dönerse kart DB'ye YAZILMAZ, tema MarkThemeIncoherent
// ile damgalanır (#151 — blockedByIdeaLens ile AYNI ilke: aksi halde
// ThemesReadyForSynthesis aynı temayı bir sonraki koşuda yeniden döner ve
// kart yeniden üretilip yeniden elenir) ve eliminations'a stage=distinctiveness
// bir satır düşer (#138).
func TestSynthesizeIdeasDistinctivenessK1BlocksAndRecords(t *testing.T) {
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

	title := "Test Doygun Sentez Fikri"
	platform, tag := "test-syn-distinct-k1", "test-syn-distinct-k1-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &fakeChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		distinctVerdict:   "fail",
		distinctCriterion: "K1",
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
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
	if found.Verdict != "fail" {
		t.Errorf("eliminations.verdict=fail beklenirdi, geldi: %q", found.Verdict)
	}
	if found.Criterion == nil || *found.Criterion != "K1" {
		t.Errorf("eliminations.criterion=K1 beklenirdi, geldi: %v", found.Criterion)
	}
	if found.Detail == nil || *found.Detail != "sorun" {
		t.Errorf("eliminations.detail kartın problem_statement'ı olmalı (%q), geldi: %v", "sorun", found.Detail)
	}

	// #151: K1 (doygunluk) ile bloklanan tema da MarkThemeIncoherent ile
	// damgalanmalı — blockedByIdeaLens ile AYNI ilke.
	var themeID int64
	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT id, incoherent_at FROM themes WHERE theme_name = $1", tag).
		Scan(&themeID, &incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt == nil {
		t.Error("K1 ile bloklanan tema MarkThemeIncoherent ile damgalanmalı (incoherent_at NULL kalmamalı)")
	}
	ready, err := st.ThemesReadyForSynthesis(ctx, cfg.MinThemeEvidence, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis: %v", err)
	}
	if themeInList(ready, themeID) {
		t.Error("K1 ile bloklanan tema bir sonraki ThemesReadyForSynthesis çağrısında DÖNMEMELİ")
	}

	// Negatif kontrol (kriter 3): aynı etiketten yeni kanıt gelince yeniden döner.
	if _, _, err := st.UpsertTheme(ctx, tag, tag); err != nil {
		t.Fatalf("yeni kanıt upsert: %v", err)
	}
	readyAfter, err := st.ThemesReadyForSynthesis(ctx, cfg.MinThemeEvidence, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (yeni kanıt sonrası): %v", err)
	}
	if !themeInList(readyAfter, themeID) {
		t.Error("aynı etiketten yeni kanıt gelip last_seen ilerleyince tema yeniden DÖNMELİ")
	}
}

// TestSynthesizeIdeasDistinctivenessErrorStillWritesCard: mercek çağrısı
// HATA verirse (ağ/kota) kart yine de yazılır, distinctiveness_* alanları
// NULL kalır (#101 v3 edge case).
func TestSynthesizeIdeasDistinctivenessErrorStillWritesCard(t *testing.T) {
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

	title := "Test Ozgunluk Hata Sentez Fikri"
	platform, tag := "test-syn-distinct-err", "test-syn-distinct-err-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &synthDistinctErrChat{response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
		"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
		"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag)}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
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

	// #151 edge case: özgünlük merceği HATA verirse tema İŞARETLENMEMELİ.
	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("özgünlük merceği hatası MarkThemeIncoherent çağırmamalı")
	}
}

// TestSynthesizeIdeasPaymentGate, #125: PreferPaymentSignal artık SERT
// ELEMİYOR, yalnız sıralama sinyali — willingness_to_pay hiç geçmeyen
// temadan da kart YAZILIR (önceki #121 davranışı: yazılmazdı). Sıralamanın
// (sinyalli tema önce) kendisi store paketinde
// (queries_payment_gate_test.go) doğrulanır.
func TestSynthesizeIdeasPaymentGate(t *testing.T) {
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

	chatFor := func(title, tag string) *fakeChat {
		return &fakeChat{response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag)}
	}

	t.Run("ödeme sinyali yok -> kart yine de yazılır (artık elenmiyor)", func(t *testing.T) {
		title := "Test Kapi Sinyalsiz Fikri"
		platform, tag := "test-paygate-off-syn", "test-paygate-off-syn-tag"
		cleanup := func() {
			st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
			st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
			st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		}
		cleanup()
		t.Cleanup(cleanup)

		setupSynthTheme(t, ctx, st, platform, tag, false)

		cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr", PreferPaymentSignal: true}
		n, err := SynthesizeIdeas(ctx, cfg, st, chatFor(title, tag))
		if err != nil {
			t.Fatalf("SynthesizeIdeas: %v", err)
		}
		if n != 1 {
			t.Errorf("ödeme sinyali olmayan temadan da kart yazılmalı (#125), geldi n=%d", n)
		}
	})

	t.Run("ödeme sinyali var -> kart yazılır", func(t *testing.T) {
		title := "Test Kapi Sinyalli Fikri"
		platform, tag := "test-paygate-on-syn", "test-paygate-on-syn-tag"
		cleanup := func() {
			st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
			st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
			st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		}
		cleanup()
		t.Cleanup(cleanup)

		setupSynthTheme(t, ctx, st, platform, tag, true)

		cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr", PreferPaymentSignal: true}
		n, err := SynthesizeIdeas(ctx, cfg, st, chatFor(title, tag))
		if err != nil {
			t.Fatalf("SynthesizeIdeas: %v", err)
		}
		if n != 1 {
			t.Errorf("ödeme sinyalli temadan kart yazılmalı, geldi n=%d", n)
		}
	})
}

func TestSynthesizeSystemPromptMentionsConstraints(t *testing.T) {
	// Ortak prompt prensibi kritik kısıtları içermeli (plan madde 7)
	for _, want := range []string{"SOFTWARE-heavy", "NOT a filter", "VERBATIM", "ORIGINAL language"} {
		if !strings.Contains(synthesizeSystemTmpl, want) {
			t.Errorf("synthesis prompt'unda %q bekleniyordu", want)
		}
	}
	// DISTINCTIVENESS RULE v1/v2'den kaldırıldı (#101 v3: synthesizeSystemTmpl'den
	// çıkarılıp ayrı bir merceğe taşındı; #138: o mercek artık K1 için
	// bloklayıcı) — bu şablonda artık geçmemeli.
	if strings.Contains(synthesizeSystemTmpl, "DISTINCTIVENESS RULE") {
		t.Error("synthesizeSystemTmpl artık DISTINCTIVENESS RULE içermemeli (#101 v3)")
	}
	// #134: DATA-ACCESS kararı artık TEK karar noktası ilkesiyle yalnız
	// lensDataAccessSystem'de veriliyor — üretim prompt'u bu kuralı taşımamalı.
	if strings.Contains(synthesizeSystemTmpl, "DATA-ACCESS RULE") {
		t.Error("synthesizeSystemTmpl artık DATA-ACCESS RULE içermemeli (#134)")
	}
	if strings.Contains(synthesizeSystemTmpl, "data-locked") {
		t.Error("synthesizeSystemTmpl artık data-locked reason'ını üretmeyi talimatlamamalı (#134)")
	}
}

// lensSeqChat, blockedByIdeaLens birim testleri için seedLenses sırasına
// göre SIRAYLA verdict/hata döner (her çağrıda listedeki bir sonraki
// eleman). errAt: -1 ise hiçbir çağrı hata vermez; aksi halde o sıradaki
// çağrı hata döner (kalanlar hiç çağrılmaz — erken çıkış doğrulaması).
type lensSeqChat struct {
	verdicts []string
	errAt    int

	calls    int
	lastTemp []float64
}

func (c *lensSeqChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *lensSeqChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	idx := c.calls
	c.calls++
	c.lastTemp = append(c.lastTemp, temp)
	if c.errAt >= 0 && idx == c.errAt {
		return "", fmt.Errorf("simulated mercek hatası")
	}
	v := "pass"
	if idx < len(c.verdicts) {
		v = c.verdicts[idx]
	}
	return fmt.Sprintf(`{"verdict":%q,"reason":"test-reason"}`, v), nil
}

// TestBlockedByIdeaLensFailBlocksAndStopsEarly, ilk merceğin "fail"
// dönmesinin kartı bloklayıp kalan mercekleri ÇAĞIRMADIĞINI doğrular (#123).
func TestBlockedByIdeaLensFailBlocksAndStopsEarly(t *testing.T) {
	chat := &lensSeqChat{verdicts: []string{"fail", "pass", "pass"}, errAt: -1}
	idea := store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	lensName, reason, blocked := blockedByIdeaLens(context.Background(), chat, &idea)
	if !blocked {
		t.Fatal("ilk mercek fail dönünce blocked=true olmalı")
	}
	if lensName != seedLenses[0].name {
		t.Errorf("bloklayan mercek adı %q beklenirdi, geldi %q", seedLenses[0].name, lensName)
	}
	if reason != "test-reason" {
		t.Errorf("mercek sebebi iletilmeli, geldi: %q", reason)
	}
	if chat.calls != 1 {
		t.Errorf("ilk fail'de erken çıkış: 1 çağrı beklenirdi, geldi %d", chat.calls)
	}
	// #131: erken çıkışta (blok) idea'ya DOKUNULMAZ — veri-erişimi merceği
	// bu senaryoda hiç çağrılmadı (ilk mercek zaten üçüncü-taraf, fail'de durdu).
	if idea.DataAccessVerdict != nil || idea.DataAccessReason != nil {
		t.Errorf("bloklanan kartta data_access_* NULL kalmalı, geldi: verdict=%v reason=%v", idea.DataAccessVerdict, idea.DataAccessReason)
	}
}

// TestBlockedByIdeaLensUnsureDoesNotBlock, "unsure"ın BLOKLAMADIĞINI ve
// tüm 3 merceğin çağrıldığını doğrular (yalnız "fail" bloklar). #131: veri-
// erişimi merceğinin (seedLenses[1]) HAM kararı idea.DataAccessVerdict/
// Reason'a yazılmalı — kart bloklanmadı, yani DB'ye yazılacak.
func TestBlockedByIdeaLensUnsureDoesNotBlock(t *testing.T) {
	chat := &lensSeqChat{verdicts: []string{"unsure", "unsure", "unsure"}, errAt: -1}
	idea := store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	if _, _, blocked := blockedByIdeaLens(context.Background(), chat, &idea); blocked {
		t.Error("unsure bloklamamalı")
	}
	if chat.calls != 3 {
		t.Errorf("3 mercek de çağrılmalı, geldi %d", chat.calls)
	}
	if idea.DataAccessVerdict == nil || *idea.DataAccessVerdict != "unsure" {
		t.Errorf("data_access_verdict=unsure beklenirdi, geldi: %v", idea.DataAccessVerdict)
	}
	if idea.DataAccessReason == nil || *idea.DataAccessReason != "test-reason" {
		t.Errorf("data_access_reason=%q beklenirdi, geldi: %v", "test-reason", idea.DataAccessReason)
	}
}

// TestBlockedByIdeaLensErrorDoesNotBlock, mercek çağrısı HATA verirse
// (ağ/kota) kartın DÜŞÜRÜLMEDİĞİNİ doğrular — hata loglanır, blok yokmuş
// gibi devam edilir (distinctivenessCheck ile aynı tutum). #131: hata
// erken dönüşe yol açtığından idea'ya HİÇ DOKUNULMAZ, data_access_* NULL kalır.
func TestBlockedByIdeaLensErrorDoesNotBlock(t *testing.T) {
	chat := &lensSeqChat{errAt: 0}
	idea := store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	if _, _, blocked := blockedByIdeaLens(context.Background(), chat, &idea); blocked {
		t.Error("mercek çağrı hatası bloklamamalı")
	}
	if chat.calls != 1 {
		t.Errorf("hatada durulmalı (kalan mercekler boşa çağrılmamalı), 1 çağrı beklenirdi, geldi %d", chat.calls)
	}
	if idea.DataAccessVerdict != nil || idea.DataAccessReason != nil {
		t.Errorf("mercek hatasında data_access_* NULL kalmalı, geldi: verdict=%v reason=%v", idea.DataAccessVerdict, idea.DataAccessReason)
	}
}

// TestBlockedByIdeaLensUsesTemperatureZero, bloklayıcı mercek çağrılarının
// (#106) sıcaklık 0 ile gittiğini doğrular.
func TestBlockedByIdeaLensUsesTemperatureZero(t *testing.T) {
	chat := &lensSeqChat{verdicts: []string{"pass", "pass", "pass"}, errAt: -1}
	idea := store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	blockedByIdeaLens(context.Background(), chat, &idea)
	if len(chat.lastTemp) != 3 {
		t.Fatalf("3 mercek çağrısı beklenirdi, geldi %d", len(chat.lastTemp))
	}
	for i, temp := range chat.lastTemp {
		if temp != 0 {
			t.Errorf("mercek çağrısı %d sıcaklık 0 olmalı, geldi: %v", i, temp)
		}
	}
}

// synthLensChat: coherence/dedup normal davranır; 3 bloklayıcı mercek
// (seedLenses sırasına göre) verdicts'teki değeri döner (boş = "pass");
// errPos'taki mercek çağrısı ise hata döner (-1 = hata yok). distinctCalls
// ve lensCalls, özgünlük merceğinin ve bloklayıcı 3 merceğin kaç
// kez çağrıldığını sayar — "mercek bloklarsa distinctivenessCheck
// çağrılmaz" ve "ilk fail'de erken çıkış" doğrulamaları için (#123).
type synthLensChat struct {
	response string
	verdicts [3]string
	errPos   int

	distinctCalls int
	lensCalls     int
}

func (f *synthLensChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *synthLensChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
	}
	if system == lensDistinctivenessSystem {
		f.distinctCalls++
		return `{"verdict":"pass","criterion":"none","reason":"ok"}`, nil
	}
	if system == dupJudgeSystem {
		return `{"same": false}`, nil
	}
	for i, l := range seedLenses {
		if system == l.system {
			f.lensCalls++
			if i == f.errPos {
				return "", fmt.Errorf("simulated mercek hatası")
			}
			v := f.verdicts[i]
			if v == "" {
				v = "pass"
			}
			return fmt.Sprintf(`{"verdict":%q,"reason":"test-blok-sebebi"}`, v), nil
		}
	}
	return f.response, nil
}

// TestSynthesizeIdeasLensFailNotWritten: 3 bloklayıcı merceğin (#123)
// ikincisi "fail" dönerse kart DB'ye YAZILMAZ, özgünlük merceği
// hiç çağrılmaz (boşa token) ve kalan 3. mercek de çağrılmaz (erken çıkış,
// çağrı sayısı doğrulanır). #151: tema da MarkThemeIncoherent ile damgalanır
// — sonraki ThemesReadyForSynthesis çağrısında dönmez, yeni kanıt gelince
// yeniden döner.
func TestSynthesizeIdeasLensFailNotWritten(t *testing.T) {
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

	title := "Test Mercek Blok Fikri"
	platform, tag := "test-syn-lens-fail", "test-syn-lens-fail-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &synthLensChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		verdicts: [3]string{"pass", "fail", "pass"},
		errPos:   -1,
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 0 {
		t.Fatalf("mercekten elenen kart SAYILMAMALI (dönüş değeri), n=0 beklenirdi, geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Error("mercekten elenen kart DB'ye yazılmamalı")
	}
	if chat.lensCalls != 2 {
		t.Errorf("ilk fail'de erken çıkış: 2 mercek çağrısı beklenirdi (3.'sü çağrılmamalı), geldi %d", chat.lensCalls)
	}
	if chat.distinctCalls != 0 {
		t.Errorf("mercek bloklarsa özgünlük merceği ÇAĞRILMAMALI, geldi %d çağrı", chat.distinctCalls)
	}

	// #138: kart zaten üretilmişti (synthesizeOne) — detail problem_statement.
	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "blocking_lens" && elims[i].Subject == title {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("mercek bloğu eliminations'a stage=blocking_lens kaydı düşürmeli")
	}
	if found.Detail == nil || *found.Detail != "sorun" {
		t.Errorf("eliminations.detail kartın problem_statement'ı olmalı (%q), geldi: %v", "sorun", found.Detail)
	}

	// #151: mercekten elenen tema MarkThemeIncoherent ile damgalanmalı —
	// aksi halde ThemesReadyForSynthesis aynı temayı bir sonraki koşuda
	// yeniden döner ve kart yeniden üretilip yeniden elenir.
	var themeID int64
	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT id, incoherent_at FROM themes WHERE theme_name = $1", tag).
		Scan(&themeID, &incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt == nil {
		t.Error("mercekten elenen tema MarkThemeIncoherent ile damgalanmalı (incoherent_at NULL kalmamalı)")
	}
	ready, err := st.ThemesReadyForSynthesis(ctx, cfg.MinThemeEvidence, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis: %v", err)
	}
	if themeInList(ready, themeID) {
		t.Error("mercekten elenen tema bir sonraki ThemesReadyForSynthesis çağrısında DÖNMEMELİ")
	}

	// Negatif kontrol (kriter 3): aynı etiketten YENİ kanıt gelip last_seen
	// ilerleyince tema yeniden sıraya girer (last_seen > incoherent_at, #149).
	if _, _, err := st.UpsertTheme(ctx, tag, tag); err != nil {
		t.Fatalf("yeni kanıt upsert: %v", err)
	}
	readyAfter, err := st.ThemesReadyForSynthesis(ctx, cfg.MinThemeEvidence, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (yeni kanıt sonrası): %v", err)
	}
	if !themeInList(readyAfter, themeID) {
		t.Error("aynı etiketten yeni kanıt gelip last_seen ilerleyince tema yeniden DÖNMELİ")
	}
}

// TestSynthesizeIdeasLensUnsureWrites: 3 mercek de "unsure" dönerse kart
// YAZILIR (yalnız "fail" bloklar) ve özgünlük merceği çağrılır.
func TestSynthesizeIdeasLensUnsureWrites(t *testing.T) {
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

	title := "Test Mercek Unsure Fikri"
	platform, tag := "test-syn-lens-unsure", "test-syn-lens-unsure-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &synthLensChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		verdicts: [3]string{"unsure", "unsure", "unsure"},
		errPos:   -1,
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 1 {
		t.Fatalf("unsure bloklamamalı, n=1 beklenirdi, geldi: %d", n)
	}
	if chat.lensCalls != 3 {
		t.Errorf("3 mercek de çağrılmalı, geldi %d", chat.lensCalls)
	}
	if chat.distinctCalls != 1 {
		t.Errorf("kart bloklanmadığından özgünlük merceği çağrılmalı, geldi %d çağrı", chat.distinctCalls)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 1 {
		t.Error("unsure verdict kartı yazmalı")
	}

	// #131: veri-erişimi merceğinin "unsure" HAM kararı karta yazılmalı —
	// organik yolda unsure bloklamaz ama görünür olmalı.
	var dataAccessVerdict, dataAccessReason *string
	if err := st.Pool.QueryRow(ctx,
		"SELECT data_access_verdict, data_access_reason FROM ideas WHERE title = $1", title).
		Scan(&dataAccessVerdict, &dataAccessReason); err != nil {
		t.Fatal(err)
	}
	if dataAccessVerdict == nil || *dataAccessVerdict != "unsure" {
		t.Errorf("data_access_verdict=unsure beklenirdi, geldi: %v", dataAccessVerdict)
	}
	if dataAccessReason == nil || *dataAccessReason != "test-blok-sebebi" {
		t.Errorf("data_access_reason=%q beklenirdi, geldi: %v", "test-blok-sebebi", dataAccessReason)
	}

	// #151 edge case: "unsure" bloklamaz, tema İŞARETLENMEMELİ.
	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("unsure verdict MarkThemeIncoherent çağırmamalı")
	}
}

// TestSynthesizeIdeasLensErrorStillWrites: bloklayıcı mercek çağrısı HATA
// verirse (ağ/kota) kart YAZILIR — bloklama YOK ilkesi (429 yüzünden kart
// kaybı olmayacak).
func TestSynthesizeIdeasLensErrorStillWrites(t *testing.T) {
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

	title := "Test Mercek Hata Sentez Fikri"
	platform, tag := "test-syn-lens-err", "test-syn-lens-err-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &synthLensChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		errPos: 0,
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 1 {
		t.Fatalf("mercek HATA verse de kart yazılmalı, n=1 beklenirdi, geldi: %d", n)
	}
	if chat.lensCalls != 1 {
		t.Errorf("hatada durulmalı (kalan mercekler çağrılmamalı), 1 çağrı beklenirdi, geldi %d", chat.lensCalls)
	}
	if chat.distinctCalls != 1 {
		t.Errorf("mercek hatası bloklamadığından özgünlük merceği çağrılmalı, geldi %d çağrı", chat.distinctCalls)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 1 {
		t.Error("mercek hatasında kart yine de yazılmalı")
	}

	// #131: mercek hatasında (ilk lens'te durulduğundan veri-erişimi merceği
	// hiç çağrılmadı) data_access_* NULL kalmalı.
	var dataAccessVerdict, dataAccessReason *string
	if err := st.Pool.QueryRow(ctx,
		"SELECT data_access_verdict, data_access_reason FROM ideas WHERE title = $1", title).
		Scan(&dataAccessVerdict, &dataAccessReason); err != nil {
		t.Fatal(err)
	}
	if dataAccessVerdict != nil || dataAccessReason != nil {
		t.Errorf("mercek hatasında data_access_* NULL kalmalı, geldi: verdict=%v reason=%v", dataAccessVerdict, dataAccessReason)
	}

	// #151 edge case: mercek çağrısı HATA verirse (blocked=false) tema
	// İŞARETLENMEMELİ — bugünkü davranış korunur.
	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("mercek hatası MarkThemeIncoherent çağırmamalı")
	}
}

// incoherentChat: tutarlılık denetimine yalnız TEK indeks (evidence 3 post
// olsa da) döner — cfg.MinThemeEvidence=3 altına düşürüp "tutarsız tema"
// dalını (#138: stage=incoherent_theme) tetiklemek için.
type incoherentChat struct{}

func (incoherentChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return incoherentChat{}.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (incoherentChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0]}`, nil
	}
	return `{"verdict":"pass","reason":"test-reason"}`, nil
}

// TestSynthesizeIdeasIncoherentThemeRecordsElimination: tutarlılık denetimi
// eşiğin altında kalınca (subset < MinThemeEvidence) tema damgalanır VE
// eliminations'a stage=incoherent_theme bir satır düşer; detail temanın en
// güçlü kanıtının başlığı olmalı (ThemeEvidence skora göre sıralı döner,
// #138).
func TestSynthesizeIdeasIncoherentThemeRecordsElimination(t *testing.T) {
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

	platform, tag := "test-syn-incoherent", "test-syn-incoherent-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	n, err := SynthesizeIdeas(ctx, cfg, st, incoherentChat{})
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 0 {
		t.Fatalf("tutarsız temadan kart üretilmemeli, n=0 beklenirdi, geldi: %d", n)
	}

	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt == nil {
		t.Error("tutarsız tema damgalanmalı (incoherent_at dolu olmalı)")
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "incoherent_theme" && elims[i].Subject == tag {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("tutarsız tema eliminations'a stage=incoherent_theme kaydı düşürmeli")
	}
	if found.Reason == nil || *found.Reason == "" {
		t.Error("eliminations.reason (tutarlılık oranı) boş olmamalı")
	}
	if found.Detail == nil || *found.Detail == "" {
		t.Error("eliminations.detail (en güçlü kanıtın başlığı) boş olmamalı")
	}
}

// vendorInternalChat: tutarlılık denetimi normal geçer (3/3), ama kart
// üretim çağrısı LLM'in "vendor-internal" skip cevabını taklit eder —
// errVendorInternal dalını (#138: stage=vendor_internal) tetiklemek için.
type vendorInternalChat struct{}

func (vendorInternalChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return vendorInternalChat{}.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (vendorInternalChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
	}
	return `{"skip": true, "reason": "vendor-internal"}`, nil
}

// TestSynthesizeIdeasVendorInternalRecordsElimination: kart üretimi
// vendor-internal skip dönünce (kart hiç üretilmez, parseIdeaResponse boş
// Idea{} döner) tema damgalanır VE eliminations'a stage=vendor_internal bir
// satır düşer; detail (kart yok) temanın en güçlü kanıtının başlığı olmalı
// (incoherent_theme ile AYNI kural, #138).
func TestSynthesizeIdeasVendorInternalRecordsElimination(t *testing.T) {
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

	platform, tag := "test-syn-vendor-internal", "test-syn-vendor-internal-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	n, err := SynthesizeIdeas(ctx, cfg, st, vendorInternalChat{})
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 0 {
		t.Fatalf("vendor-internal temadan kart üretilmemeli, n=0 beklenirdi, geldi: %d", n)
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "vendor_internal" && elims[i].Subject == tag {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("vendor-internal eliminations'a stage=vendor_internal kaydı düşürmeli")
	}
	if found.Detail == nil || *found.Detail == "" {
		t.Error("eliminations.detail (kart yok, en güçlü kanıtın başlığı) boş olmamalı")
	}
}

// dataLockedChat: tutarlılık denetimi normal geçer (3/3), ama kart üretim
// çağrısı LLM'in eski/kendiliğinden "data-locked" skip cevabını taklit eder
// — errDataLocked dalını (#134: stage=blocking_lens) tetiklemek için.
type dataLockedChat struct{}

func (dataLockedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return dataLockedChat{}.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (dataLockedChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
	}
	return `{"skip": true, "reason": "data-locked"}`, nil
}

// TestSynthesizeIdeasDataLockedDoesNotEmbedTheme (#134): kart üretim
// prompt'undan kaldırılan eski DATA-ACCESS kuralı hâlâ (eski model davranışı
// ya da modelin kendiliğinden) "data-locked" skip döndürürse, tema
// vendor-internal'ın AKSİNE kalıcı gömülmez (incoherent_at NULL kalır) —
// bir sonraki koşuda organik yoldan (lensDataAccessSystem dahil) yeniden
// değerlendirilebilir. Yine de eliminations'a stage=blocking_lens bir kayıt
// düşer (best-effort görünürlük).
func TestSynthesizeIdeasDataLockedDoesNotEmbedTheme(t *testing.T) {
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

	platform, tag := "test-syn-data-locked", "test-syn-data-locked-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	n, err := SynthesizeIdeas(ctx, cfg, st, dataLockedChat{})
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 0 {
		t.Fatalf("data-locked temadan kart üretilmemeli, n=0 beklenirdi, geldi: %d", n)
	}

	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("data-locked skip temayı KALICI GÖMMEMELİ (vendor-internal'ın aksine, #134)")
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "blocking_lens" && elims[i].Subject == tag {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("data-locked skip eliminations'a stage=blocking_lens kaydı düşürmeli")
	}
	if found.Reason == nil || *found.Reason != "data-locked" {
		t.Errorf("eliminations.reason=data-locked beklenirdi, geldi: %v", found.Reason)
	}
	if found.Detail == nil || *found.Detail == "" {
		t.Error("eliminations.detail (kart yok, en güçlü kanıtın başlığı) boş olmamalı")
	}
}

// TestSynthesizeIdeasEliminationWriteErrorDoesNotStopPipeline: eliminations
// yazımı hata verirse (writeElimination sahtesiyle simüle edilir) pipeline
// DURMAZ — K1 bloğu kararı (kart yazılmaz) etkilenmeden uygulanmaya devam
// eder, SynthesizeIdeas hata döndürmez (#138: best-effort ilkesi).
func TestSynthesizeIdeasEliminationWriteErrorDoesNotStopPipeline(t *testing.T) {
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

	title := "Test Kayit Hatasi Sentez Fikri"
	platform, tag := "test-syn-elim-write-err", "test-syn-elim-write-err-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	orig := writeElimination
	writeElimination = func(ctx context.Context, st *store.Store, e store.Elimination) (int64, error) {
		return 0, fmt.Errorf("simulated eliminations yazım hatası")
	}
	t.Cleanup(func() { writeElimination = orig })

	setupSynthTheme(t, ctx, st, platform, tag, false)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &fakeChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		distinctVerdict:   "fail",
		distinctCriterion: "K1",
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("eliminations yazım hatası SynthesizeIdeas'ı durdurmamalı, geldi: %v", err)
	}
	if n != 0 {
		t.Fatalf("K1 bloğu eliminations yazım hatasından ETKİLENMEMELİ, n=0 beklenirdi, geldi: %d", n)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Error("K1 bloğu eliminations yazım hatasında da kartı yazmamalı")
	}
}
