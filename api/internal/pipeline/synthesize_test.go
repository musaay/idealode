package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"

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

func TestParseIdeaResponseRejectsEmpty(t *testing.T) {
	if _, err := parseIdeaResponse(`{"title":"  "}`); err == nil {
		t.Error("boş title hata dönmeli")
	}
	if _, err := parseIdeaResponse(`garbage`); err == nil {
		t.Error("JSON olmayan yanıt hata dönmeli")
	}
}

// fakeChat, synthesize entegrasyon testi için sabit yanıt döner; tutarlılık
// denetimi çağrısına ise tüm post'ları tutarlı sayan bir cevap verir.
type fakeChat struct{ response string }

func (f *fakeChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	if system == coherenceSystem {
		return `{"indices":[0,1,2]}`, nil
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

func TestCoherentSubsetRejectsGarbage(t *testing.T) {
	chat := &indicesChat{response: `garbage`}
	if _, err := coherentSubset(context.Background(), chat, []store.RawPost{{Title: "a"}}); err == nil {
		t.Error("JSON olmayan tutarlılık cevabı hata dönmeli")
	}
}

// indicesChat, tutarlılık denetimi birim testleri için sabit cevap döner.
type indicesChat struct{ response string }

func (c *indicesChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
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

func TestSynthesizeSystemPromptMentionsConstraints(t *testing.T) {
	// Ortak prompt prensibi kritik kısıtları içermeli (plan madde 7)
	for _, want := range []string{"SOFTWARE-heavy", "NOT a filter", "VERBATIM", "ORIGINAL language"} {
		if !strings.Contains(synthesizeSystemTmpl, want) {
			t.Errorf("synthesis prompt'unda %q bekleniyordu", want)
		}
	}
}
