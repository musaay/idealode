package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ActiveSources, ingest'in işleyeceği aktif kaynakları döner.
func (s *Store) ActiveSources(ctx context.Context) ([]Source, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, platform, community, COALESCE(category, ''), active, COALESCE(last_seen_ref, '')
		FROM sources
		WHERE active
		ORDER BY platform, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Platform, &src.Community, &src.Category, &src.Active, &src.LastSeenRef); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// InsertRawPosts, post'ları idempotent yazar (UNIQUE(platform, source_ref)
// üzerinde ON CONFLICT DO NOTHING) ve gerçekten eklenen satır sayısını döner.
func (s *Store) InsertRawPosts(ctx context.Context, posts []RawPost) (int, error) {
	if len(posts) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, p := range posts {
		batch.Queue(`
			INSERT INTO raw_posts (platform, source_ref, community, title, body, author, url, score, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (platform, source_ref) DO NOTHING`,
			p.Platform, p.SourceRef, p.Community, p.Title, p.Body, p.Author, p.URL, p.Score, p.CreatedAt)
	}

	results := s.Pool.SendBatch(ctx, batch)
	defer results.Close()

	inserted := 0
	for range posts {
		tag, err := results.Exec()
		if err != nil {
			return inserted, fmt.Errorf("raw_posts insert: %w", err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// UpdateSourceLastSeen, kaynağın ilerleme imlecini günceller.
func (s *Store) UpdateSourceLastSeen(ctx context.Context, sourceID int64, ref string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE sources SET last_seen_ref = $2, updated_at = now() WHERE id = $1`,
		sourceID, ref)
	return err
}
