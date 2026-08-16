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

// UnanalyzedPosts, henüz post_analysis kaydı olmayan post'ları döner
// (eskiden yeniye — imleç mantığıyla uyumlu).
func (s *Store) UnanalyzedPosts(ctx context.Context, limit int) ([]RawPost, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.platform, rp.source_ref, rp.community, rp.title, rp.body,
		       COALESCE(rp.author, ''), COALESCE(rp.url, ''), COALESCE(rp.score, 0),
		       COALESCE(rp.created_at, rp.fetched_at)
		FROM raw_posts rp
		LEFT JOIN post_analysis pa ON pa.post_id = rp.id
		WHERE pa.id IS NULL
		ORDER BY rp.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		if err := rows.Scan(&p.ID, &p.Platform, &p.SourceRef, &p.Community, &p.Title,
			&p.Body, &p.Author, &p.URL, &p.Score, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertPostAnalyses, classification sonuçlarını idempotent yazar
// (post_id UNIQUE üzerinde ON CONFLICT DO NOTHING).
func (s *Store) InsertPostAnalyses(ctx context.Context, analyses []PostAnalysis) error {
	if len(analyses) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, a := range analyses {
		// nil slice pgx'te SQL NULL'a eşlenir ve NOT NULL kolonu ihlal
		// eder; çağıran ne gönderirse göndersin boş diziye indirgenir.
		tags := a.DomainTags
		if tags == nil {
			tags = []string{}
		}
		batch.Queue(`
			INSERT INTO post_analysis
				(post_id, classification, problem_summary, target_audience,
				 domain_tags, willingness_to_pay, prefiltered)
			VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7)
			ON CONFLICT (post_id) DO NOTHING`,
			a.PostID, a.Classification, a.ProblemSummary, a.TargetAudience,
			tags, a.WillingnessToPay, a.Prefiltered)
	}
	results := s.Pool.SendBatch(ctx, batch)
	defer results.Close()
	for range analyses {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("post_analysis insert: %w", err)
		}
	}
	return nil
}

// UnthemedAnalyses, henüz hiçbir temaya bağlanmamış sinyal analizlerini
// döner (yalnız pain_point / feature_request; noise/complaint tema kurmaz).
func (s *Store) UnthemedAnalyses(ctx context.Context, limit int) ([]PostAnalysis, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT pa.post_id, pa.classification, pa.domain_tags
		FROM post_analysis pa
		WHERE pa.classification IN ('pain_point', 'feature_request')
		  AND COALESCE(array_length(pa.domain_tags, 1), 0) > 0
		  AND NOT EXISTS (SELECT 1 FROM theme_posts tp WHERE tp.post_id = pa.post_id)
		ORDER BY pa.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PostAnalysis
	for rows.Next() {
		var a PostAnalysis
		if err := rows.Scan(&a.PostID, &a.Classification, &a.DomainTags); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertTheme, tag için temayı bulur/oluşturur ve last_seen'i tazeler.
func (s *Store) UpsertTheme(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO themes (theme_name) VALUES ($1)
		ON CONFLICT (theme_name) DO UPDATE SET last_seen = now()
		RETURNING id`, name).Scan(&id)
	return id, err
}

// LinkThemePost, post'u temaya idempotent bağlar.
func (s *Store) LinkThemePost(ctx context.Context, themeID, postID int64) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO theme_posts (theme_id, post_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, themeID, postID)
	return err
}

// RefreshThemeStats, frequency'yi theme_posts sayısıyla eşitler.
func (s *Store) RefreshThemeStats(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE themes t
		SET frequency = c.cnt
		FROM (SELECT theme_id, count(*) AS cnt FROM theme_posts GROUP BY theme_id) c
		WHERE c.theme_id = t.id`)
	return err
}

// ThemesReadyForSynthesis, frekans eşiğini geçmiş ve henüz idea üretilmemiş
// temaları döner (en yüksek frekans önce).
func (s *Store) ThemesReadyForSynthesis(ctx context.Context, minEvidence, limit int) ([]Theme, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.theme_name, t.frequency
		FROM themes t
		WHERE t.frequency >= $1
		  AND NOT EXISTS (SELECT 1 FROM ideas i WHERE i.source_theme_id = t.id)
		ORDER BY t.frequency DESC, t.id
		LIMIT $2`, minEvidence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Theme
	for rows.Next() {
		var t Theme
		if err := rows.Scan(&t.ID, &t.Name, &t.Frequency); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThemeEvidence, temayı destekleyen ham post'ları döner (en yüksek skor önce).
func (s *Store) ThemeEvidence(ctx context.Context, themeID int64, limit int) ([]RawPost, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.platform, rp.source_ref, rp.community, rp.title, rp.body,
		       COALESCE(rp.author, ''), COALESCE(rp.url, ''), COALESCE(rp.score, 0),
		       COALESCE(rp.created_at, rp.fetched_at)
		FROM theme_posts tp
		JOIN raw_posts rp ON rp.id = tp.post_id
		WHERE tp.theme_id = $1
		ORDER BY rp.score DESC NULLS LAST, rp.id DESC
		LIMIT $2`, themeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		if err := rows.Scan(&p.ID, &p.Platform, &p.SourceRef, &p.Community, &p.Title,
			&p.Body, &p.Author, &p.URL, &p.Score, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IdeaTitleExists, birebir başlık eşleşmesine bakar (Faz 0 naif dedup;
// pg_trgm benzerlik dedup'u Faz 1).
func (s *Store) IdeaTitleExists(ctx context.Context, title string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ideas WHERE lower(title) = lower($1))`, title).Scan(&exists)
	return exists, err
}

// InsertIdea, idea card'ı yazar ve id döner.
func (s *Store) InsertIdea(ctx context.Context, i Idea) (int64, error) {
	// nil slice'lar SQL NULL'a eşlenir; NOT NULL kolonlar için boş diziye
	// indirgenir (bkz. InsertPostAnalyses'teki aynı koruma).
	if i.DomainTags == nil {
		i.DomainTags = []string{}
	}
	if i.ExampleQuotes == nil {
		i.ExampleQuotes = []string{}
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO ideas
			(title, problem_statement, proposed_solution, target_user, evidence_count,
			 example_quotes, source_type, source_theme_id, created_by_user_id,
			 urgency_score, monetization_signal, known_competitors_ai_guess, domain_tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''), $13)
		RETURNING id`,
		i.Title, i.ProblemStatement, i.ProposedSolution, i.TargetUser, i.EvidenceCount,
		i.ExampleQuotes, i.SourceType, i.SourceThemeID, nil,
		i.UrgencyScore, i.MonetizationSignal, i.KnownCompetitorsAIGuess, i.DomainTags).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ideas insert: %w", err)
	}
	return id, nil
}

// ListIdeas, idea card'ları tema adıyla birlikte döner (yeniden eskiye) —
// Faz 0'da `dump` komutu ve kalite kapısı değerlendirmesi bununla beslenir.
func (s *Store) ListIdeas(ctx context.Context, limit int) ([]Idea, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.title, i.problem_statement, i.proposed_solution, i.target_user,
		       i.evidence_count, i.example_quotes, i.source_type, i.source_theme_id,
		       COALESCE(i.urgency_score, 0), COALESCE(i.monetization_signal, 0),
		       COALESCE(i.known_competitors_ai_guess, ''), i.domain_tags,
		       COALESCE(t.theme_name, ''), i.created_at
		FROM ideas i
		LEFT JOIN themes t ON t.id = i.source_theme_id
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Idea
	for rows.Next() {
		var i Idea
		if err := rows.Scan(&i.ID, &i.Title, &i.ProblemStatement, &i.ProposedSolution,
			&i.TargetUser, &i.EvidenceCount, &i.ExampleQuotes, &i.SourceType,
			&i.SourceThemeID, &i.UrgencyScore, &i.MonetizationSignal,
			&i.KnownCompetitorsAIGuess, &i.DomainTags, &i.SourceTheme, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// UpdateSourceLastSeen, kaynağın ilerleme imlecini günceller.
func (s *Store) UpdateSourceLastSeen(ctx context.Context, sourceID int64, ref string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE sources SET last_seen_ref = $2, updated_at = now() WHERE id = $1`,
		sourceID, ref)
	return err
}
