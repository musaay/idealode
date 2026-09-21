package store

import (
	"context"
	"testing"
	"time"
)

// sp, testte *string literal üretmek için kısa yardımcı.
func sp(s string) *string { return &s }

// TestInsertEliminationRoundTrip, InsertElimination'ın yazdığı satırın
// EliminationsSince ile aynen geri geldiğini doğrular — nullable alanlar
// (criterion/reason/detail) hem dolu hem NULL durumda (#138).
func TestInsertEliminationRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subjectFull := "test-elim-full"
	subjectBare := "test-elim-bare"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject IN ($1, $2)", subjectFull, subjectBare)
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)

	fullID, err := s.InsertElimination(ctx, Elimination{
		Stage: "distinctiveness", Subject: subjectFull, Verdict: "fail",
		Criterion: sp("K1"), Reason: sp("10+ benzer ürün var"), Detail: sp("kartın problem cümlesi"),
	})
	if err != nil {
		t.Fatalf("InsertElimination (dolu): %v", err)
	}
	if fullID == 0 {
		t.Error("InsertElimination pozitif id dönmeli")
	}

	bareID, err := s.InsertElimination(ctx, Elimination{
		Stage: "incoherent_theme", Subject: subjectBare, Verdict: "fail",
	})
	if err != nil {
		t.Fatalf("InsertElimination (bare): %v", err)
	}
	if bareID == 0 {
		t.Error("InsertElimination pozitif id dönmeli")
	}

	elims, err := s.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}

	var full, bare *Elimination
	for i := range elims {
		switch elims[i].Subject {
		case subjectFull:
			full = &elims[i]
		case subjectBare:
			bare = &elims[i]
		}
	}
	if full == nil {
		t.Fatal("dolu kayıt EliminationsSince'te bulunamadı")
	}
	if full.Stage != "distinctiveness" || full.Verdict != "fail" {
		t.Errorf("dolu kayıt stage/verdict yanlış: %+v", full)
	}
	if full.Criterion == nil || *full.Criterion != "K1" {
		t.Errorf("dolu kayıt criterion=K1 beklenirdi, geldi: %v", full.Criterion)
	}
	if full.Reason == nil || *full.Reason != "10+ benzer ürün var" {
		t.Errorf("dolu kayıt reason yanlış, geldi: %v", full.Reason)
	}
	if full.Detail == nil || *full.Detail != "kartın problem cümlesi" {
		t.Errorf("dolu kayıt detail yanlış, geldi: %v", full.Detail)
	}
	if full.OccurredAt.IsZero() {
		t.Error("occurred_at otomatik dolmalı (DEFAULT now())")
	}

	if bare == nil {
		t.Fatal("bare kayıt EliminationsSince'te bulunamadı")
	}
	if bare.Stage != "incoherent_theme" || bare.Verdict != "fail" {
		t.Errorf("bare kayıt stage/verdict yanlış: %+v", bare)
	}
	if bare.Criterion != nil {
		t.Errorf("bare kayıtta criterion NULL olmalı, geldi: %v", bare.Criterion)
	}
	if bare.Reason != nil {
		t.Errorf("bare kayıtta reason NULL olmalı, geldi: %v", bare.Reason)
	}
	if bare.Detail != nil {
		t.Errorf("bare kayıtta detail NULL olmalı, geldi: %v", bare.Detail)
	}
}

// TestEliminationsSinceExcludesOlder, since sınırının ÖNCESİNDEKİ kayıtları
// dışladığını doğrular — gelecekteki bir "since" ile hiçbir satırın
// dönmemesi gerekir (#138).
func TestEliminationsSinceExcludesOlder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subject := "test-elim-excl"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", subject)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := s.InsertElimination(ctx, Elimination{
		Stage: "blocking_lens", Subject: subject, Verdict: "fail",
	}); err != nil {
		t.Fatalf("InsertElimination: %v", err)
	}

	future := time.Now().Add(time.Hour)
	elims, err := s.EliminationsSince(ctx, future)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	for _, e := range elims {
		if e.Subject == subject {
			t.Error("gelecekteki since ile bu kayıt DÖNMEMELİ")
		}
	}
}

// TestEliminationCountsSinceByStage, sayımın stage bazında doğru
// gruplandığını doğrular (#138: `run` koşu sonu özet log satırı bu
// sorguyla beslenir).
func TestEliminationCountsSinceByStage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	subjectPrefix := "test-elim-counts-"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject LIKE $1", subjectPrefix+"%")
	}
	cleanup()
	t.Cleanup(cleanup)

	since := time.Now().Add(-time.Minute)

	rows := []Elimination{
		{Stage: "incoherent_theme", Subject: subjectPrefix + "1", Verdict: "fail"},
		{Stage: "incoherent_theme", Subject: subjectPrefix + "2", Verdict: "fail"},
		{Stage: "blocking_lens", Subject: subjectPrefix + "3", Verdict: "fail"},
		{Stage: "distinctiveness", Subject: subjectPrefix + "4", Verdict: "fail", Criterion: sp("K1")},
		{Stage: "distinctiveness", Subject: subjectPrefix + "5", Verdict: "fail", Criterion: sp("K3")},
		{Stage: "distinctiveness", Subject: subjectPrefix + "6", Verdict: "fail", Criterion: sp("K1")},
	}
	for _, r := range rows {
		if _, err := s.InsertElimination(ctx, r); err != nil {
			t.Fatalf("InsertElimination: %v", err)
		}
	}

	counts, err := s.EliminationCountsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationCountsSince: %v", err)
	}
	if counts["incoherent_theme"] != 2 {
		t.Errorf("incoherent_theme=2 beklenirdi, geldi: %d", counts["incoherent_theme"])
	}
	if counts["blocking_lens"] != 1 {
		t.Errorf("blocking_lens=1 beklenirdi, geldi: %d", counts["blocking_lens"])
	}
	if counts["distinctiveness"] != 3 {
		t.Errorf("distinctiveness=3 beklenirdi, geldi: %d", counts["distinctiveness"])
	}
	if counts["vendor_internal"] != 0 {
		t.Errorf("vendor_internal=0 (hiç yazılmadı) beklenirdi, geldi: %d", counts["vendor_internal"])
	}
}

// TestEliminationStageCheckConstraint, tanınmayan bir stage değerinin CHECK
// kısıtına çarpıp hata döndürdüğünü doğrular — şema savunması (#138).
func TestEliminationStageCheckConstraint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.InsertElimination(ctx, Elimination{
		Stage: "unknown_stage", Subject: "test-elim-badstage", Verdict: "fail",
	})
	if err == nil {
		t.Error("bilinmeyen stage CHECK kısıtına çarpıp hata dönmeli")
	}
}
