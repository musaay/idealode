// Package pipeline, IdeaLode'un aşamalarını (ingest -> analyze -> synthesize)
// orkestre eder.
package pipeline

import (
	"context"
	"log"
	"sync"

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
	// Product Hunt token ister; token yokken registry'ye girmez, ingest
	// kaynağı "connector yok" logu ile atlar (kabul kriteri: pipeline düşmez).
	if cfg.ProductHuntToken != "" {
		ph := connector.NewProductHunt(cfg.ProductHuntToken)
		m[ph.Platform()] = ph
	}
	return m
}

// rawPostInserter, Ingest'in kaynak başına ihtiyaç duyduğu iki store
// operasyonunu soyutlar — canlı DB olmadan fake ile test edilebilir olsun
// diye (bkz. ingest_test.go). *store.Store bu arayüzü zaten sağlar.
type rawPostInserter interface {
	InsertRawPosts(ctx context.Context, posts []store.RawPost) (int, error)
	UpdateSourceLastSeen(ctx context.Context, sourceID int64, ref string) error
}

// Ingest, aktif kaynakları dolaşıp yeni post'ları raw_posts'a yazar.
// Tek kaynağın hatası diğerlerini durdurmaz; toplam eklenen sayısı döner.
func Ingest(ctx context.Context, cfg *config.Config, st *store.Store) (int, error) {
	sources, err := st.ActiveSources(ctx)
	if err != nil {
		return 0, err
	}
	return ingestSources(ctx, sources, Connectors(cfg), st)
}

// groupSourcesByPlatform, kaynakları platforma göre gruplar; her grup
// içinde mevcut sıra korunur. Dönen platform listesi ilk görülme sırasına
// göredir (deterministik iterasyon için).
func groupSourcesByPlatform(sources []store.Source) ([]string, map[string][]store.Source) {
	var platforms []string
	groups := map[string][]store.Source{}
	for _, src := range sources {
		if _, ok := groups[src.Platform]; !ok {
			platforms = append(platforms, src.Platform)
		}
		groups[src.Platform] = append(groups[src.Platform], src)
	}
	return platforms, groups
}

// ingestSources, kaynakları platforma göre gruplar ve PLATFORMLAR ARASI
// PARALEL, PLATFORM İÇİ SERİ şekilde işler: aynı platformun kaynakları
// rate-limit paylaşımı nedeniyle kasıtlı olarak tek goroutine'de eski
// sırasıyla çekilir; farklı platformlar birbirinden bağımsız goroutine'lerde
// eşzamanlı ilerler. Toplam eklenen sayaç mutex ile korunur.
func ingestSources(ctx context.Context, sources []store.Source, connectors map[string]connector.SourceConnector, st rawPostInserter) (int, error) {
	platforms, groups := groupSourcesByPlatform(sources)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)

	for _, platform := range platforms {
		group := groups[platform]
		wg.Add(1)
		go func(group []store.Source) {
			defer wg.Done()
			for _, src := range group {
				if ctx.Err() != nil {
					return
				}
				inserted := ingestOneSource(ctx, connectors, st, src)
				mu.Lock()
				total += inserted
				mu.Unlock()
			}
		}(group)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return total, ctx.Err()
	}
	return total, nil
}

// ingestOneSource, tek bir kaynağı çeker ve yazar; hata durumunda 0 döner
// ve log basar, çağıranı durdurmaz (kaynak başına hata izolasyonu).
func ingestOneSource(ctx context.Context, connectors map[string]connector.SourceConnector, st rawPostInserter, src store.Source) int {
	conn, ok := connectors[src.Platform]
	if !ok {
		log.Printf("ingest: %s için connector yok, atlandı (community=%q)", src.Platform, src.Community)
		return 0
	}

	posts, newRef, err := conn.FetchNew(ctx, src)
	if err != nil {
		log.Printf("ingest: %s/%q HATA: %v — diğer kaynaklarla devam", src.Platform, src.Community, err)
		return 0
	}

	inserted, err := st.InsertRawPosts(ctx, posts)
	if err != nil {
		log.Printf("ingest: %s/%q yazım HATASI: %v", src.Platform, src.Community, err)
		return 0
	}
	// İmleç yalnız başarılı yazım sonrası ilerletilir — hata durumunda
	// bir sonraki koşu aynı aralığı yeniden dener.
	if newRef != "" {
		if err := st.UpdateSourceLastSeen(ctx, src.ID, newRef); err != nil {
			log.Printf("ingest: %s/%q last_seen_ref güncellenemedi: %v", src.Platform, src.Community, err)
		}
	}

	log.Printf("ingest: %s/%q -> %d çekildi, %d yeni", src.Platform, src.Community, len(posts), inserted)
	return inserted
}
