// idealode — çok kaynaklı yazılım fikri öneri pipeline'ı (tek binary).
//
// Subcommand'lar:
//
//	ingest      aktif kaynaklardan yeni post'ları çek ve raw_posts'a yaz
//	analyze     ön-filtre + Groq classification -> post_analysis
//	synthesize  tema gruplama + idea synthesis -> themes/ideas
//	generate    kullanıcı bazlı ai_generated üretim (Faz 2)
//	run         ingest -> analyze -> synthesize sırayla
//	dump        idea card'ları JSON olarak stdout'a dök
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/pipeline"
	"github.com/musaay/idealode/api/internal/store"
)

const usageText = `idealode — çok kaynaklı yazılım fikri öneri pipeline'ı

Kullanım: idealode <komut>

Komutlar:
  ingest      aktif kaynaklardan yeni post'ları çek (raw_posts)
  analyze     ön-filtre + Groq classification (post_analysis)
  synthesize  tema gruplama + idea synthesis (themes, ideas)
  generate    kullanıcı bazlı ai_generated üretim (Faz 2)
  run         ingest -> analyze -> synthesize sırayla çalıştırır
  dump        idea card'ları JSON olarak stdout'a döker

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
	case "ingest", "analyze", "synthesize", "generate", "run", "dump":
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
	case "generate":
		return cmdGenerate(ctx, cfg)
	case "run":
		if err := cmdIngest(ctx, cfg); err != nil {
			return fmt.Errorf("ingest: %w", err)
		}
		if err := cmdAnalyze(ctx, cfg); err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		if err := cmdSynthesize(ctx, cfg); err != nil {
			return fmt.Errorf("synthesize: %w", err)
		}
		return nil
	case "dump":
		return cmdDump(ctx, cfg)
	}
	return fmt.Errorf("bilinmeyen komut: %q", cmd)
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

func cmdGenerate(ctx context.Context, cfg *config.Config) error {
	return fmt.Errorf("generate Faz 2 kapsamında (bkz. issue #20)")
}

func cmdDump(ctx context.Context, cfg *config.Config) error {
	return fmt.Errorf("henüz uygulanmadı (bkz. issue #9)")
}
