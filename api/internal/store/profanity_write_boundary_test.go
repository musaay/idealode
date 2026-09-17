package store

import (
	"context"
	"testing"
)

// TestInsertIdeaFiltersProfaneQuotes, store yazım sınırındaki
// profanity.Filter garantisini doğrular (#100): küfürlü bir example_quotes
// satırıyla InsertIdea çağrıldığında, okunan kartta o satır YOKTUR; temiz
// satır değişmeden kalır.
func TestInsertIdeaFiltersProfaneQuotes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-profanity-insert", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"},
		ExampleQuotes: []string{"clean quote here", "this app is fucking broken"},
		UrgencyScore:  3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	var quotes []string
	if err := s.Pool.QueryRow(ctx, "SELECT example_quotes FROM ideas WHERE id = $1", id).Scan(&quotes); err != nil {
		t.Fatalf("okuma: %v", err)
	}
	if len(quotes) != 1 || quotes[0] != "clean quote here" {
		t.Errorf("example_quotes = %v, want [\"clean quote here\"] (küfürlü satır düşmeliydi)", quotes)
	}
}

// TestInsertIdeaAllQuotesProfaneYieldsEmptySlice, tüm alıntılar
// filtrelendiğinde kolonun NULL değil boş dilim olarak yazıldığını
// doğrular (nil-slice tuzağı guard'ı, #100 kenar durumu).
func TestInsertIdeaAllQuotesProfaneYieldsEmptySlice(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-profanity-all-dropped", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"},
		ExampleQuotes: []string{"this app is fucking broken", "siktir git"},
		UrgencyScore:  3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	var quotes []string
	if err := s.Pool.QueryRow(ctx, "SELECT example_quotes FROM ideas WHERE id = $1", id).Scan(&quotes); err != nil {
		t.Fatalf("okuma: %v", err)
	}
	if quotes == nil {
		t.Fatal("example_quotes NULL döndü, want boş (non-nil) dilim")
	}
	if len(quotes) != 0 {
		t.Errorf("example_quotes = %v, want boş dilim", quotes)
	}
}

// TestSetIdeaLocalEvidenceFiltersProfanity, SetIdeaLocalEvidence'ın da
// yazmadan önce profanity.Filter uyguladığını doğrular (#100).
func TestSetIdeaLocalEvidenceFiltersProfanity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-profanity-set-evidence", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"}, UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	if err := s.SetIdeaLocalEvidence(ctx, id, []string{"clean evidence line", "such an idiot move"}); err != nil {
		t.Fatalf("SetIdeaLocalEvidence: %v", err)
	}

	var evidence []string
	if err := s.Pool.QueryRow(ctx, "SELECT local_evidence FROM ideas WHERE id = $1", id).Scan(&evidence); err != nil {
		t.Fatalf("okuma: %v", err)
	}
	if len(evidence) != 1 || evidence[0] != "clean evidence line" {
		t.Errorf("local_evidence = %v, want [\"clean evidence line\"]", evidence)
	}
}

// TestAppendIdeaLocalEvidenceFiltersProfanity, AppendIdeaLocalEvidence'ın
// da yazmadan önce profanity.Filter uyguladığını, temiz satırları
// SİLMEDEN eklediğini doğrular (#100).
func TestAppendIdeaLocalEvidenceFiltersProfanity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-profanity-append-evidence", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"}, UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("InsertIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	if err := s.SetIdeaLocalEvidence(ctx, id, []string{"first clean line"}); err != nil {
		t.Fatalf("SetIdeaLocalEvidence: %v", err)
	}
	if err := s.AppendIdeaLocalEvidence(ctx, id, []string{"second clean line", "this is fucking annoying"}); err != nil {
		t.Fatalf("AppendIdeaLocalEvidence: %v", err)
	}

	var evidence []string
	if err := s.Pool.QueryRow(ctx, "SELECT local_evidence FROM ideas WHERE id = $1", id).Scan(&evidence); err != nil {
		t.Fatalf("okuma: %v", err)
	}
	if len(evidence) != 2 || evidence[0] != "first clean line" || evidence[1] != "second clean line" {
		t.Errorf("local_evidence = %v, want [\"first clean line\" \"second clean line\"]", evidence)
	}
}
