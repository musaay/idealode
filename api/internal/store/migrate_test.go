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
