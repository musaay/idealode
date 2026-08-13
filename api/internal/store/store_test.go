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
