package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- scrub-quotes (#100)
//
// `idealode scrub-quotes` geriye dönük temizlik komutunun DB erişim
// katmanı — filtre mantığı (profanity.Filter) BURADA DEĞİL,
// pipeline.ScrubQuotes'ta çalışır (store paketi DB'nin dışını bilmez,
// RethemeResolve/rethemeTargets ile aynı katman ayrımı).

// ScrubQuoteRow, scrub-quotes'un tarama kümesindeki TEK kart — mevcut
// (filtre öncesi) example_quotes/local_evidence hali.
type ScrubQuoteRow struct {
	ID            int64
	Title         string
	ExampleQuotes []string
	LocalEvidence []string
}

// ScrubQuoteRows, TÜM kartların id/title/example_quotes/local_evidence
// alanlarını id sırasıyla döner.
func (s *Store) ScrubQuoteRows(ctx context.Context) ([]ScrubQuoteRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, title, example_quotes, local_evidence FROM ideas ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScrubQuoteRow
	for rows.Next() {
		var r ScrubQuoteRow
		if err := rows.Scan(&r.ID, &r.Title, &r.ExampleQuotes, &r.LocalEvidence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScrubQuoteWrite, ApplyQuoteScrub'ın TEK kart güncellemesi — filtrelenmiş
// (temizlenmiş) example_quotes/local_evidence.
type ScrubQuoteWrite struct {
	ID            int64
	ExampleQuotes []string
	LocalEvidence []string
}

// ApplyQuoteScrub, verilen güncellemeleri TEK transaction içinde yazar
// (#100) — RethemeResolve ile aynı atomiklik ilkesi: yarıda kesilirse hiçbir
// kart güncellenmemiş sayılır. nil slice guard korunur (NOT NULL DEFAULT
// '{}' kolonları, CLAUDE.md nil-slice tuzağı).
func (s *Store) ApplyQuoteScrub(ctx context.Context, writes []ScrubQuoteWrite) error {
	if len(writes) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback'te DIŞARIDAN gelen ctx DEĞİL context.Background() kullanılır
	// (bkz. RethemeResolve'daki aynı gerekçe: iptal/timeout ctx'iyle
	// ROLLBACK göndermek de başarısız olabilir).
	defer func() { _ = tx.Rollback(context.Background()) }()

	batch := &pgx.Batch{}
	for _, w := range writes {
		quotes := w.ExampleQuotes
		if quotes == nil {
			quotes = []string{}
		}
		evidence := w.LocalEvidence
		if evidence == nil {
			evidence = []string{}
		}
		batch.Queue(`UPDATE ideas SET example_quotes = $2, local_evidence = $3, updated_at = now() WHERE id = $1`,
			w.ID, quotes, evidence)
	}
	br := tx.SendBatch(ctx, batch)
	for range writes {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("scrub-quotes güncelleme: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("scrub-quotes güncelleme (batch kapanışı): %w", err)
	}
	return tx.Commit(ctx)
}
