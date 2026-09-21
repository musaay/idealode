package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// pgTestStore, gerçek Postgres'e karşı koşan entegrasyon testleri için
// DATABASE_URL ile bağlanır; env yoksa test atlanır (#98).
func pgTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL tanımlı değil")
	}
	s, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestMomentumCandidatesRealSQL, #98'in konusu: `recent` CTE'sindeki
// alias'sız COALESCE kolonları dış SELECT'in author/url/score isimleriyle
// okumasını Postgres'te "column does not exist" hatasıyla kırıyordu (fake
// store testleri gerçek SQL çalıştırmadığı için bunu yakalayamıyordu).
// Bu test gerçek Postgres'e karşı en az bir satır dönmesini doğrular.
func TestMomentumCandidatesRealSQL(t *testing.T) {
	s := pgTestStore(t)
	ctx := context.Background()

	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'github_trending' AND source_ref LIKE 'idealode-test/%'")
	})

	now := time.Now().UTC()
	posts := []RawPost{
		{
			Platform:  "github_trending",
			SourceRef: "idealode-test/momentum-repo:" + now.Format("2006-01-02"),
			Community: "daily",
			Title:     "idealode-test/momentum-repo",
			Body:      "",
			Author:    "idealode-test-owner",
			URL:       "https://github.com/idealode-test/momentum-repo",
			Score:     123,
			CreatedAt: now,
		},
		{
			Platform:  "github_trending",
			SourceRef: "idealode-test/momentum-repo:" + now.AddDate(0, 0, -1).Format("2006-01-02"),
			Community: "daily",
			Title:     "idealode-test/momentum-repo",
			Body:      "",
			Author:    "idealode-test-owner",
			URL:       "https://github.com/idealode-test/momentum-repo",
			Score:     100,
			CreatedAt: now.AddDate(0, 0, -1),
		},
		{
			Platform:  "github_trending",
			SourceRef: "idealode-test/other-repo:" + now.Format("2006-01-02"),
			Community: "daily",
			Title:     "idealode-test/other-repo",
			Body:      "",
			Author:    "idealode-test-owner-2",
			URL:       "https://github.com/idealode-test/other-repo",
			Score:     50,
			CreatedAt: now,
		},
	}
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("InsertRawPosts: %v", err)
	}

	got, err := s.MomentumCandidates(ctx, "idealode-test/momentum-repo", nil, 10)
	if err != nil {
		t.Fatalf("MomentumCandidates hata döndürmemeliydi (alias eksikliği #98): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("MomentumCandidates en az bir satır dönmeliydi")
	}

	found := false
	for _, p := range got {
		if p.SourceRef == "idealode-test/momentum-repo:"+now.Format("2006-01-02") {
			found = true
			if p.Author != "idealode-test-owner" {
				t.Errorf("Author = %q, beklenen idealode-test-owner", p.Author)
			}
			if p.URL != "https://github.com/idealode-test/momentum-repo" {
				t.Errorf("URL = %q, beklenen https://github.com/idealode-test/momentum-repo", p.URL)
			}
			if p.Score != 123 {
				t.Errorf("Score = %d, beklenen 123 (en güncel gün, DISTINCT ON)", p.Score)
			}
		}
	}
	if !found {
		t.Errorf("beklenen repo satırı sonuçlarda yok: %+v", got)
	}
}
