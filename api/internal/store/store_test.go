package store

import (
	"context"
	"errors"
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

// TestUnanalyzedPostsExcludesRadarSeed, ProcessSeeds'in işaret satırlarının
// (#56) analiz kuyruğuna hiç girmediğini doğrular.
func TestUnanalyzedPostsExcludesRadarSeed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test-radar-seed", SourceRef: "trs-1", Community: "c", Title: "normal post"},
		{Platform: "radar_seed", SourceRef: "trs-2", Community: "radar", Title: "tohum işaret"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ('test-radar-seed', 'radar_seed') AND source_ref LIKE 'trs-%'")
	})
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out, err := s.UnanalyzedPosts(ctx, 1000)
	if err != nil {
		t.Fatalf("UnanalyzedPosts: %v", err)
	}
	for _, p := range out {
		if p.Platform == "radar_seed" {
			t.Fatalf("radar_seed post analiz kuyruğuna girmemeli: %+v", p)
		}
	}
	found := false
	for _, p := range out {
		if p.SourceRef == "trs-1" {
			found = true
		}
	}
	if !found {
		t.Error("normal post analiz kuyruğunda görünmeli")
	}
}

// TestArchivedIdeaHidden, arşivlenmiş (archived_at dolu) kartın galeri
// listesinde görünmediğini ve GetIdea ile ErrNotFound döndüğünü doğrular;
// arşivsiz kart etkilenmemeli (#74).
func TestArchivedIdeaHidden(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	active, err := s.InsertIdea(ctx, Idea{
		Title:            "test-archive-active",
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("active insert: %v", err)
	}
	archived, err := s.InsertIdea(ctx, Idea{
		Title:            "test-archive-archived",
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("archived insert: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", active, archived)
	})

	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET archived_at = now() WHERE id = $1", archived); err != nil {
		t.Fatalf("archive update: %v", err)
	}

	// Liste: arşivli kart yok, arşivsiz kart var.
	out, err := s.ListIdeasFiltered(ctx, IdeaFilter{Limit: 200})
	if err != nil {
		t.Fatalf("ListIdeasFiltered: %v", err)
	}
	var sawActive, sawArchived bool
	for _, i := range out {
		if i.ID == active {
			sawActive = true
		}
		if i.ID == archived {
			sawArchived = true
		}
	}
	if !sawActive {
		t.Error("arşivsiz kart listede görünmeli")
	}
	if sawArchived {
		t.Error("arşivli kart listede görünmemeli")
	}

	// Detay: arşivsiz 200, arşivli ErrNotFound.
	if _, err := s.GetIdea(ctx, active, ""); err != nil {
		t.Errorf("GetIdea(arşivsiz): beklenmeyen hata: %v", err)
	}
	if _, err := s.GetIdea(ctx, archived, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIdea(arşivli): ErrNotFound bekleniyordu, alınan: %v", err)
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
