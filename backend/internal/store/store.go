// Package store, IdeaLode'un Postgres erişim katmanı. Veriler gerçek
// `idealode` şemasında yaşar; bağlantı search_path ile şemaya sabitlenir.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxConns: cv-search ile aynı Railway Postgres instance'ı paylaşılır;
// pipeline azarsa cv-search'ün bağlantılarını yiyemesin diye düşük sınır.
const MaxConns = 5

// Store, pgx pool'unu sarar; tüm sorgular bu tip üzerinden gider.
type Store struct {
	Pool *pgxpool.Pool
}

// Connect, search_path=idealode,public ve max_conns sınırıyla pool açar
// ve bağlantıyı ping'ler.
func Connect(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL parse: %w", err)
	}
	cfg.MaxConns = MaxConns
	cfg.ConnConfig.RuntimeParams["search_path"] = "idealode, public"

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("veritabanına ulaşılamadı: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close pool'u kapatır.
func (s *Store) Close() { s.Pool.Close() }
