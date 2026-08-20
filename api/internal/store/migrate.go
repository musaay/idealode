package store

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed migrate_sql/001_init.sql
var migration001 string

//go:embed migrate_sql/002_seed.sql
var migration002 string

//go:embed migrate_sql/003_more_hn_sources.sql
var migration003 string

//go:embed migrate_sql/004_market_derived.sql
var migration004 string

//go:embed migrate_sql/005_tr_sources.sql
var migration005 string

//go:embed migrate_sql/006_theme_coherence.sql
var migration006 string

//go:embed migrate_sql/007_idea_dedup.sql
var migration007 string

// Migrate, embed edilmiş migration dosyalarını sırayla, DB'ye tek seferlik
// uygular. `idealode migrate` subcommand'i tarafından elle tetiklenir —
// uygulama normal çalışmasında (ingest/analyze/synthesize) OTOMATİK
// çalışmaz (plan kararı: manuel migration).
//
// Şema henüz yokken çalıştığı için normal pool yerine search_path'siz, tek
// seferlik bir bağlantı kullanır; simple protocol çok-ifadeli SQL dosyasını
// tek seferde (psql gibi) çalıştırabilmek için gerekli.
func Migrate(ctx context.Context, databaseURL string) error {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("DATABASE_URL parse: %w", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("bağlantı: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, migration001); err != nil {
		return fmt.Errorf("001_init.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration002); err != nil {
		return fmt.Errorf("002_seed.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration003); err != nil {
		return fmt.Errorf("003_more_hn_sources.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration004); err != nil {
		return fmt.Errorf("004_market_derived.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration005); err != nil {
		return fmt.Errorf("005_tr_sources.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration006); err != nil {
		return fmt.Errorf("006_theme_coherence.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration007); err != nil {
		return fmt.Errorf("007_idea_dedup.sql: %w", err)
	}
	return nil
}
