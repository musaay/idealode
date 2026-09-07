package store

import (
	"context"
	"os"
	"testing"
)

// TestMigrateIdempotent, Migrate'in iki kez art arda hatasız çalıştığını
// doğrular (CLAUDE.md: "her migration idempotent olmalı, tüm dosyalar her
// koşuda yeniden çalıştırılır") — 014_distinctiveness.sql dahil tüm zincir.
// TEST_DATABASE_URL gerektirir (canlı prod'a DOKUNMAZ, ayrı test DB'si).
func TestMigrateIdempotent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate (idempotent olmalı): %v", err)
	}

	// 014'ün eklediği kolonlar var mı ve nullable mı — InsertBlendedIdea
	// bu alanları YAZMAZ, kolonlar NOT NULL olursa o insert kırılır.
	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer s.Close()

	rows, err := s.Pool.Query(ctx, `
		SELECT column_name, is_nullable FROM information_schema.columns
		WHERE table_schema = 'idealode' AND table_name = 'ideas'
		  AND column_name IN ('distinctiveness_verdict', 'distinctiveness_criterion', 'distinctiveness_reason')`)
	if err != nil {
		t.Fatalf("information_schema sorgusu: %v", err)
	}
	defer rows.Close()

	found := map[string]string{}
	for rows.Next() {
		var col, nullable string
		if err := rows.Scan(&col, &nullable); err != nil {
			t.Fatal(err)
		}
		found[col] = nullable
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, col := range []string{"distinctiveness_verdict", "distinctiveness_criterion", "distinctiveness_reason"} {
		nullable, ok := found[col]
		if !ok {
			t.Errorf("kolon eksik: %s", col)
			continue
		}
		if nullable != "YES" {
			t.Errorf("%s NULL olabilmeli (blend kartları bu alanları hiç yazmaz), is_nullable=%s", col, nullable)
		}
	}
}

// TestMigratePublishedBackfillOnlyOnce, 015_published.sql'in backfill'inin
// yalnız kolon İLK eklendiğinde çalıştığını doğrular (reviewer bulgusu):
// migrate() -> beklemedeki kart insert edilir (published_at NULL) ->
// migrate() TEKRAR -> kart HÂLÂ beklemede olmalı. Sabit tarihe göre koşullu
// eski backfill bu ikinci migrate'te kartı sessizce yayınlıyordu.
func TestMigratePublishedBackfillOnlyOnce(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}

	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer s.Close()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-migrate-backfill-pending", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	// Insert sonrası kolon zaten var, kart beklemede (published_at NULL).
	var publishedAt *string
	if err := s.Pool.QueryRow(ctx, "SELECT published_at::text FROM ideas WHERE id = $1", id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil {
		t.Fatalf("insert sonrası kart beklemede olmalı (published_at NULL), geldi: %v", *publishedAt)
	}

	// İkinci Migrate: backfill kolon zaten var olduğundan çalışmamalı,
	// beklemedeki kart yayınlanmamalı.
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate: %v", err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT published_at::text FROM ideas WHERE id = $1", id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil {
		t.Errorf("ikinci Migrate beklemedeki kartı sessizce yayınlamamalı (published_at hâlâ NULL olmalı), geldi: %v", *publishedAt)
	}
}
