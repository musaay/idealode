package pipeline

import (
	"context"
	"os"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

// Gerçek DB isteyen entegrasyon testi; TEST_DATABASE_URL yoksa atlanır.
func TestGroupThemesIntegration(t *testing.T) {
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
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-theme'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name LIKE 'test-tt-%'")
	}
	cleanup()
	t.Cleanup(cleanup)

	posts := []store.RawPost{
		{Platform: "test-theme", SourceRef: "p1", Community: "c", Title: "a"},
		{Platform: "test-theme", SourceRef: "p2", Community: "c", Title: "b"},
		{Platform: "test-theme", SourceRef: "p3", Community: "c", Title: "c"},
	}
	if _, err := st.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("posts: %v", err)
	}
	var ids []int64
	rows, _ := st.Pool.Query(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-theme' ORDER BY id")
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	analyses := []store.PostAnalysis{
		{PostID: ids[0], Classification: "pain_point", DomainTags: []string{"test-tt-invoice", "extra"}},
		{PostID: ids[1], Classification: "feature_request", DomainTags: []string{"test-tt-invoice"}},
		{PostID: ids[2], Classification: "noise", DomainTags: []string{"test-tt-noise"}},
	}
	if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatalf("analyses: %v", err)
	}

	linked, err := GroupThemes(ctx, st)
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != 2 {
		t.Errorf("2 post bağlanmalıydı (noise hariç), geldi: %d", linked)
	}

	var freq int
	if err := st.Pool.QueryRow(ctx,
		"SELECT frequency FROM themes WHERE theme_name = 'test-tt-invoice'").Scan(&freq); err != nil {
		t.Fatalf("tema oluşmamış: %v", err)
	}
	if freq != 2 {
		t.Errorf("frequency theme_posts ile tutarlı olmalı (2), geldi: %d", freq)
	}

	var noiseTheme int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM themes WHERE theme_name = 'test-tt-noise'").Scan(&noiseTheme)
	if noiseTheme != 0 {
		t.Error("noise sınıfı tema oluşturmamalı")
	}

	// idempotency: ikinci koşuda yeni bağlantı yok
	linked2, err := GroupThemes(ctx, st)
	if err != nil {
		t.Fatalf("GroupThemes (2. koşu): %v", err)
	}
	if linked2 != 0 {
		t.Errorf("ikinci koşu 0 bağlamalı, geldi: %d", linked2)
	}
}
