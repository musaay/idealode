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

// TestDistinctivenessAdviseUsesTemperatureZero, özgünlük merceğinin (yargı
// çağrısı, #106) sıcaklık 0 ile çağrıldığını doğrular.
func TestDistinctivenessAdviseUsesTemperatureZero(t *testing.T) {
	chat := &fakeChat{}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}
	if err := distinctivenessAdvise(context.Background(), chat, idea); err != nil {
		t.Fatalf("distinctivenessAdvise: %v", err)
	}
	if got := chat.lastTemp[lensDistinctivenessSystem]; got != 0 {
		t.Errorf("özgünlük merceği sıcaklık 0 ile çağrılmalı, geldi: %v", got)
	}
}

// fakeChat, synthesize entegrasyon testi için sabit yanıt döner; tutarlılık
// denetimi çağrısına ise tüm post'ları tutarlı sayan bir cevap verir.
// distinctVerdict/distinctCriterion, kart-sonrası ADVISORY özgünlük
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
	if _, err := GroupThemes(ctx, st); err != nil {
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
// kart-sonrası ADVISORY özgünlük merceği (lensDistinctivenessSystem)
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
// distinctiveness testlerinin ortak fikstürü.
func setupSynthTheme(t *testing.T, ctx context.Context, st *store.Store, platform, tag string) {
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
		})
	}
	rows.Close()
	if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupThemes(ctx, st); err != nil {
		t.Fatal(err)
	}
}

// TestSynthesizeIdeasDistinctivenessFailStillWritesCard: özgünlük merceği
// "fail" (K2) dönse bile kart YAZILIR (#101 v3: bloklama YOK, işaretle) —
// tema damgalanmaz (MarkThemeIncoherent bu yüzden çağrılmaz), alanlar
// karta doğru yazılır.
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
	}
	cleanup()
	t.Cleanup(cleanup)

	setupSynthTheme(t, ctx, st, platform, tag)

	cfg := &config.Config{MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr"}
	chat := &fakeChat{
		response: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
			"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
			"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag),
		distinctVerdict:   "fail",
		distinctCriterion: "K2",
	}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
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
	if criterion == nil || *criterion != "K2" {
		t.Errorf("distinctiveness_criterion=K2 beklenirdi, geldi: %v", criterion)
	}
	if reason == nil || *reason == "" {
		t.Error("distinctiveness_reason boş olmamalı")
	}

	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt != nil {
		t.Error("advisory mercek MarkThemeIncoherent çağırmamalı (bloklama YOK)")
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

	setupSynthTheme(t, ctx, st, platform, tag)

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
}

func TestSynthesizeSystemPromptMentionsConstraints(t *testing.T) {
	// Ortak prompt prensibi kritik kısıtları içermeli (plan madde 7)
	for _, want := range []string{"SOFTWARE-heavy", "NOT a filter", "VERBATIM", "ORIGINAL language"} {
		if !strings.Contains(synthesizeSystemTmpl, want) {
			t.Errorf("synthesis prompt'unda %q bekleniyordu", want)
		}
	}
	// DISTINCTIVENESS RULE v1/v2'den kaldırıldı (#101 v3: bloklama YOK,
	// mercek advisory) — prompt'ta artık geçmemeli.
	if strings.Contains(synthesizeSystemTmpl, "DISTINCTIVENESS RULE") {
		t.Error("synthesizeSystemTmpl artık DISTINCTIVENESS RULE içermemeli (#101 v3)")
	}
}
