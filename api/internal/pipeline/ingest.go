// Package pipeline, IdeaLode'un aşamalarını (ingest -> analyze -> synthesize)
// orkestre eder.
package pipeline

import (
	"context"
	"log"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/connector"
	"github.com/musaay/idealode/api/internal/store"
)

// Connectors, konfigürasyona göre kullanılabilir connector'ları kurar.
// Product Hunt Faz 1'de eklenir; Reddit wired-ama-uykuda (env boşken
// registry'ye hiç girmez).
func Connectors(cfg *config.Config) map[string]connector.SourceConnector {
	m := map[string]connector.SourceConnector{}
	hn := connector.NewHackerNews()
	m[hn.Platform()] = hn
	se := connector.NewStackExchange(cfg.StackExchangeKey)
	m[se.Platform()] = se
	gh := connector.NewGitHub(cfg.GitHubToken)
	m[gh.Platform()] = gh
	gp := connector.NewGooglePlay()
	m[gp.Platform()] = gp
	tp := connector.NewTechnopat()
	m[tp.Platform()] = tp
	return m
}

// Ingest, aktif kaynakları dolaşıp yeni post'ları raw_posts'a yazar.
// Tek kaynağın hatası diğerlerini durdurmaz; toplam eklenen sayısı döner.
func Ingest(ctx context.Context, cfg *config.Config, st *store.Store) (int, error) {
	sources, err := st.ActiveSources(ctx)
	if err != nil {
		return 0, err
	}

	connectors := Connectors(cfg)
	total := 0

	for _, src := range sources {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		conn, ok := connectors[src.Platform]
		if !ok {
			log.Printf("ingest: %s için connector yok, atlandı (community=%q)", src.Platform, src.Community)
			continue
		}

		posts, newRef, err := conn.FetchNew(ctx, src)
		if err != nil {
			log.Printf("ingest: %s/%q HATA: %v — diğer kaynaklarla devam", src.Platform, src.Community, err)
			continue
		}

		inserted, err := st.InsertRawPosts(ctx, posts)
		if err != nil {
			log.Printf("ingest: %s/%q yazım HATASI: %v", src.Platform, src.Community, err)
			continue
		}
		// İmleç yalnız başarılı yazım sonrası ilerletilir — hata durumunda
		// bir sonraki koşu aynı aralığı yeniden dener.
		if newRef != "" {
			if err := st.UpdateSourceLastSeen(ctx, src.ID, newRef); err != nil {
				log.Printf("ingest: %s/%q last_seen_ref güncellenemedi: %v", src.Platform, src.Community, err)
			}
		}

		log.Printf("ingest: %s/%q -> %d çekildi, %d yeni", src.Platform, src.Community, len(posts), inserted)
		total += inserted
	}
	return total, nil
}
