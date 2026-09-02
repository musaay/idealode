// idealode — çok kaynaklı yazılım fikri öneri pipeline'ı (tek binary).
//
// Subcommand'lar:
//
//	ingest      aktif kaynaklardan yeni post'ları çek ve raw_posts'a yaz
//	analyze     ön-filtre + Groq classification -> post_analysis
//	synthesize  tema gruplama + idea synthesis -> themes/ideas
//	seeds       pazar tohumlarını (radar-seeds.jsonl) 3 mercekten geçir -> market_derived kart
//	generate    kullanıcı bazlı ai_generated üretim (Faz 2)
//	run         ingest -> analyze -> synthesize sırayla
//	serve       web arayüzünü sunar (galeri + kart detayı, salt okunur)
//	dump        idea card'ları JSON olarak stdout'a dök
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/pipeline"
	"github.com/musaay/idealode/api/internal/store"
	"github.com/musaay/idealode/api/internal/web"
)

const usageText = `idealode — çok kaynaklı yazılım fikri öneri pipeline'ı

Kullanım: idealode <komut>

Komutlar:
  ingest      aktif kaynaklardan yeni post'ları çek (raw_posts)
  analyze     ön-filtre + Groq classification (post_analysis)
  synthesize  tema gruplama + idea synthesis (themes, ideas)
  seeds       pazar tohumlarını 3 mercekten geçir (market_derived kart)
  generate    kullanıcı bazlı ai_generated üretim (Faz 2)
  run         ingest -> analyze -> synthesize -> fuse -> seeds sırayla çalıştırır
  serve       web arayüzünü sunar (galeri + kart detayı); PORT, varsayılan 8080
  dump        idea card'ları JSON olarak stdout'a döker
  migrate     embed edilmiş .sql dosyalarını DB'ye uygular (elle tetiklenir)

Konfigürasyon ortam değişkenlerinden okunur; bkz. .env.example
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("idealode: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	cmd := os.Args[1]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return
	case "ingest", "analyze", "synthesize", "seeds", "generate", "fuse", "run", "serve", "dump", "migrate":
		// aşağıda dispatch
	default:
		fmt.Fprintf(os.Stderr, "bilinmeyen komut: %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("konfigürasyon: %v", err)
	}

	if err := dispatch(ctx, cfg, cmd); err != nil {
		log.Fatalf("%s: %v", cmd, err)
	}
}

func dispatch(ctx context.Context, cfg *config.Config, cmd string) error {
	switch cmd {
	case "ingest":
		return cmdIngest(ctx, cfg)
	case "analyze":
		return cmdAnalyze(ctx, cfg)
	case "synthesize":
		return cmdSynthesize(ctx, cfg)
	case "seeds":
		return cmdSeeds(ctx, cfg)
	case "generate":
		return cmdGenerate(ctx, cfg)
	case "run":
		// Advisory lock (#15): çakışan koşu (örn. uzun süren önceki cron)
		// varsa bu koşu sessizce atlanır — veri yarışı ve çift iş önlenir.
		lockSt, err := store.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		release, ok, err := lockSt.AcquireRunLock(ctx)
		if err != nil {
			lockSt.Close()
			return fmt.Errorf("koşu kilidi: %w", err)
		}
		if !ok {
			log.Printf("run: başka bir koşu kilidi tutuyor — bu koşu atlandı")
			lockSt.Close()
			return nil
		}
		defer func() {
			release()
			lockSt.Close()
		}()

		if err := cmdIngest(ctx, cfg); err != nil {
			return fmt.Errorf("ingest: %w", err)
		}
		if err := cmdAnalyze(ctx, cfg); err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		if err := cmdSynthesize(ctx, cfg); err != nil {
			return fmt.Errorf("synthesize: %w", err)
		}
		if err := cmdFuse(ctx, cfg); err != nil {
			return fmt.Errorf("fuse: %w", err)
		}
		if err := cmdSeeds(ctx, cfg); err != nil {
			return fmt.Errorf("seeds: %w", err)
		}
		return nil
	case "fuse":
		return cmdFuse(ctx, cfg)
	case "serve":
		return cmdServe(ctx, cfg)
	case "dump":
		return cmdDump(ctx, cfg)
	case "migrate":
		return cmdMigrate(ctx, cfg)
	}
	return fmt.Errorf("bilinmeyen komut: %q", cmd)
}

// cmdMigrate, embed edilmiş .sql dosyalarını DB'ye elle tetiklenerek uygular
// (bkz. internal/store/migrate.go). Otomatik/örtük çalışmaz.
func cmdMigrate(ctx context.Context, cfg *config.Config) error {
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.Printf("migrate tamam")
	return nil
}

// Aşağıdaki komutlar sonraki issue'larda doldurulur (Faz 0 sırası: #2 şema,
// #3-4 connector'lar, #5-6 analyze, #7-8 synthesize, #9 run/dump).

func cmdIngest(ctx context.Context, cfg *config.Config) error {
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	n, err := pipeline.Ingest(ctx, cfg, st)
	log.Printf("ingest tamam: %d yeni post", n)
	return err
}

func cmdAnalyze(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireGroq(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	n, err := pipeline.Analyze(ctx, cfg, st, chat)
	log.Printf("analyze tamam: %d post işlendi", n)
	return err
}

// cmdFuse, market_derived kartlara yerel talep kanıtı eşleştirir (#43).
func cmdFuse(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireGroq(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	n, err := pipeline.FuseEvidence(ctx, cfg, st, chat)
	if err != nil {
		return err
	}
	log.Printf("fuse tamam: %d kart işlendi", n)
	return nil
}

func cmdSynthesize(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireGroq(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if _, err := pipeline.GroupThemes(ctx, st); err != nil {
		return fmt.Errorf("tema gruplama: %w", err)
	}
	chat := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	n, err := pipeline.SynthesizeIdeas(ctx, cfg, st, chat)
	log.Printf("synthesize tamam: %d yeni idea", n)
	return err
}

// cmdSeeds, elle küratörlüğü yapılan pazar tohumlarını (radar-seeds.jsonl)
// 3 mercekten geçirip market_derived kart üretir (#56).
func cmdSeeds(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireGroq(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := llm.NewGroq(cfg.GroqAPIKey, cfg.GroqModel)
	n, err := pipeline.ProcessSeeds(ctx, cfg, st, chat, pipeline.RadarSeedsJSONL)
	if err != nil {
		return err
	}
	log.Printf("seeds tamam: %d yeni idea", n)
	return nil
}

// cmdServe, salt okunur web arayüzünü ayağa kaldırır (#21). Pipeline
// çalıştırmaz; Railway'de `run` cron servisinden ayrı bir servis olarak
// koşar. Adres PORT ortam değişkeninden okunur (varsayılan 8080).
func cmdServe(ctx context.Context, cfg *config.Config) error {
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return web.NewServer(st).ListenAndServe(ctx, ":"+port)
}

func cmdGenerate(ctx context.Context, cfg *config.Config) error {
	return fmt.Errorf("generate Faz 2 kapsamında (bkz. issue #20)")
}

func cmdDump(ctx context.Context, cfg *config.Config) error {
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	ideas, err := st.ListIdeas(ctx, 500)
	if err != nil {
		return err
	}
	if ideas == nil {
		ideas = []store.Idea{}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ideas); err != nil {
		return err
	}
	log.Printf("dump: %d idea card", len(ideas))
	return nil
}
