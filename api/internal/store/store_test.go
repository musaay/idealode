package store

import (
	"context"
	"os"
	"testing"
)

// Gerçek DB isteyen testler TEST_DATABASE_URL ile koşulur; yoksa atlanır.
// Örn: TEST_DATABASE_URL=postgres://postgres@127.0.0.1:54329/idealode_test go test ./internal/store
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	s, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestInsertRawPostsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test", SourceRef: "t-1", Community: "c", Title: "a"},
		{Platform: "test", SourceRef: "t-2", Community: "c", Title: "b"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test'")
	})

	n1, err := s.InsertRawPosts(ctx, posts)
	if err != nil {
		t.Fatalf("ilk insert: %v", err)
	}
	n2, err := s.InsertRawPosts(ctx, posts)
	if err != nil {
		t.Fatalf("ikinci insert: %v", err)
	}
	if n1 != 2 || n2 != 0 {
		t.Errorf("idempotency: ilk=%d (beklenen 2), ikinci=%d (beklenen 0)", n1, n2)
	}
}

func TestConnectSearchPath(t *testing.T) {
	s := testStore(t)

	var path string
	if err := s.Pool.QueryRow(context.Background(), "SHOW search_path").Scan(&path); err != nil {
		t.Fatalf("SHOW search_path: %v", err)
	}
	if path != "idealode, public" {
		t.Errorf("search_path beklenen değil: %q", path)
	}

	// search_path sayesinde şema öneki olmadan erişilebilmeli
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sources").Scan(&n); err != nil {
		t.Fatalf("sources sorgusu (search_path üzerinden): %v", err)
	}
	if n == 0 {
		t.Errorf("seed sonrası sources boş olmamalı")
	}
}
