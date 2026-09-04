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

//go:embed migrate_sql/008_evidence_fusion.sql
var migration008 string

//go:embed migrate_sql/009_fusion_sources.sql
var migration009 string

//go:embed migrate_sql/010_anon_chat.sql
var migration010 string

//go:embed migrate_sql/011_archive_ideas.sql
var migration011 string

//go:embed migrate_sql/012_github_trending.sql
var migration012 string

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
	if _, err := conn.Exec(ctx, migration008); err != nil {
		return fmt.Errorf("008_evidence_fusion.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration009); err != nil {
		return fmt.Errorf("009_fusion_sources.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration010); err != nil {
		return fmt.Errorf("010_anon_chat.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration011); err != nil {
		return fmt.Errorf("011_archive_ideas.sql: %w", err)
	}
	if _, err := conn.Exec(ctx, migration012); err != nil {
		return fmt.Errorf("012_github_trending.sql: %w", err)
	}
	return nil
}
