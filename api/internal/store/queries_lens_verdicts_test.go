package store

import (
	"context"
	"testing"
	"time"
)

// TestInsertIdeaLensVerdictsRoundTrip, InsertIdea'nın yazdığı lens_verdicts
// jsonb dizisinin (#164) GetIdea ile AYNEN (lens/prompt_version/subject/
// verdict/reason/at) geri geldiğini doğrular — pass/fail/unsure/error dört
// durumun da okunabildiğini kanıtlar (DoD: "pass/fail/error üç durum da
// okunabiliyor").
func TestInsertIdeaLensVerdictsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	at := time.Now().UTC().Truncate(time.Second)
	verdicts := []LensVerdict{
		{Lens: "üçüncü-taraf inşa edilebilirlik", PromptVersion: "v1", Subject: "card", Verdict: "pass", Reason: "sebep 1", At: at},
		{Lens: "veri-erişimi", PromptVersion: "v1", Subject: "card", Verdict: "unsure", Reason: "sebep 2", At: at},
		{Lens: "pazar-işlerliği", PromptVersion: "v1", Subject: "seed", Verdict: "error", Reason: "simulated 429", At: at},
		{Lens: "özgünlük", PromptVersion: "v1", Subject: "card", Verdict: "fail", Reason: "K3 sebep", At: at},
	}

	title := "Test Lens Verdicts Round Trip Karti"
	id, err := s.InsertIdea(ctx, Idea{
		Title: title, ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 1,
		LensVerdicts: verdicts,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	// GetIdea yalnız yayındaki kartları döner (published_at IS NOT NULL) —
	// test kartı PO onayını simüle etmek için yayına alınır.
	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("published_at güncellenemedi: %v", err)
	}

	got, err := s.GetIdea(ctx, id, "")
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if len(got.LensVerdicts) != len(verdicts) {
		t.Fatalf("lens_verdicts uzunluğu = %d, beklenen %d: %+v", len(got.LensVerdicts), len(verdicts), got.LensVerdicts)
	}
	for i, want := range verdicts {
		got := got.LensVerdicts[i]
		if got.Lens != want.Lens || got.PromptVersion != want.PromptVersion || got.Subject != want.Subject ||
			got.Verdict != want.Verdict || got.Reason != want.Reason {
			t.Errorf("eleman %d = %+v, beklenen %+v", i, got, want)
		}
		if !got.At.Equal(want.At) {
			t.Errorf("eleman %d At = %v, beklenen %v", i, got.At, want.At)
		}
	}
}

// TestInsertIdeaLensVerdictsNilBecomesEmptyArray, LensVerdicts nil
// bırakıldığında DB'ye NULL DEĞİL, jsonb "[]" (boş dizi) yazıldığını ve
// okurken de nil'e DEĞİL boş dizisine indirgendiğini doğrular (CLAUDE.md
// nil-slice tuzağı — jsonb için "null" literaline karşı guard).
func TestInsertIdeaLensVerdictsNilBecomesEmptyArray(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	title := "Test Lens Verdicts Nil Guard Karti"
	id, err := s.InsertIdea(ctx, Idea{
		Title: title, ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 1,
		// LensVerdicts kasıtlı olarak boş bırakıldı (nil).
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	var raw string
	if err := s.Pool.QueryRow(ctx, "SELECT lens_verdicts::text FROM ideas WHERE id = $1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "[]" {
		t.Errorf("DB'de lens_verdicts = %q, \"[]\" beklenirdi (NULL/\"null\" DEĞİL)", raw)
	}

	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("published_at güncellenemedi: %v", err)
	}
	got, err := s.GetIdea(ctx, id, "")
	if err != nil {
		t.Fatalf("GetIdea: %v", err)
	}
	if got.LensVerdicts == nil {
		t.Error("scanIdea sonrası LensVerdicts nil OLMAMALI (boş dizi guard'ı)")
	}
	if len(got.LensVerdicts) != 0 {
		t.Errorf("LensVerdicts boş beklenirdi, geldi: %+v", got.LensVerdicts)
	}
}

// TestInsertEliminationCheckAndVerdictsRoundTrip, eliminations.check (#164
// — ayrılmış anahtar sözcük, tırnaklı kolon) ve eliminations.verdicts
// jsonb'nin (kart hiç yazılmadan elenen durumun TEK kalıcı yeri) AYNEN geri
// geldiğini doğrular.
func TestInsertEliminationCheckAndVerdictsRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subject := "test-elim-check-verdicts"
	cleanup := func() { s.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", subject) }
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)
	at := time.Now().UTC().Truncate(time.Second)
	verdicts := []LensVerdict{
		{Lens: "üçüncü-taraf inşa edilebilirlik", PromptVersion: "v1", Subject: "seed", Verdict: "pass", Reason: "ok", At: at},
		{Lens: "veri-erişimi", PromptVersion: "v1", Subject: "seed", Verdict: "fail", Reason: "scraping", At: at},
	}

	check := "veri-erişimi"
	if _, err := s.InsertElimination(ctx, Elimination{
		Stage: "blocking_lens", Subject: subject, Verdict: "fail",
		Reason: sp("scraping"), Check: &check, Verdicts: verdicts,
	}); err != nil {
		t.Fatalf("InsertElimination: %v", err)
	}

	elims, err := s.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *Elimination
	for i := range elims {
		if elims[i].Subject == subject {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("kayıt EliminationsSince'te bulunamadı")
	}
	if found.Check == nil || *found.Check != check {
		t.Errorf("check = %v, beklenen %q", found.Check, check)
	}
	if len(found.Verdicts) != len(verdicts) {
		t.Fatalf("verdicts uzunluğu = %d, beklenen %d: %+v", len(found.Verdicts), len(verdicts), found.Verdicts)
	}
	for i, want := range verdicts {
		got := found.Verdicts[i]
		if got.Lens != want.Lens || got.Verdict != want.Verdict || got.Subject != want.Subject {
			t.Errorf("verdicts[%d] = %+v, beklenen %+v", i, got, want)
		}
	}
}

// TestInsertEliminationCheckNilAndVerdictsEmpty, check nil ve verdicts nil
// bırakıldığında check NULL, verdicts jsonb "[]" (NULL DEĞİL) yazıldığını
// doğrular — incoherent_theme/vendor_internal gibi mercek-dışı elemeler
// bu şekilde yazılır (bkz. synthesize.go recordElimination çağrıları).
func TestInsertEliminationCheckNilAndVerdictsEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subject := "test-elim-check-nil"
	cleanup := func() { s.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", subject) }
	cleanup()
	t.Cleanup(cleanup)

	id, err := s.InsertElimination(ctx, Elimination{
		Stage: "incoherent_theme", Subject: subject, Verdict: "fail",
	})
	if err != nil {
		t.Fatalf("InsertElimination: %v", err)
	}

	var checkVal *string
	var verdictsRaw string
	if err := s.Pool.QueryRow(ctx, `SELECT "check", verdicts::text FROM eliminations WHERE id = $1`, id).Scan(&checkVal, &verdictsRaw); err != nil {
		t.Fatal(err)
	}
	if checkVal != nil {
		t.Errorf("check NULL beklenirdi, geldi: %q", *checkVal)
	}
	if verdictsRaw != "[]" {
		t.Errorf("verdicts = %q, \"[]\" beklenirdi (NULL/\"null\" DEĞİL)", verdictsRaw)
	}
}
