// idealode — çok kaynaklı yazılım fikri öneri pipeline'ı (tek binary).
//
// Subcommand'lar:
//
//	ingest      aktif kaynaklardan yeni post'ları çek ve raw_posts'a yaz
//	analyze     ön-filtre + LLM classification -> post_analysis
//	synthesize  tema gruplama + idea synthesis -> themes/ideas
//	seeds       pazar tohumlarını (radar-seeds.jsonl) 3 mercekten geçir -> market_derived kart
//	generate    kullanıcı bazlı ai_generated üretim (Faz 2)
//	run         ingest -> analyze -> synthesize sırayla
//	retheme     eski tip temalardaki kartsız gönderileri temalarından çözer (elle, #136)
//	api         JSON API'yi sunar (DATABASE_URL'i gören TEK süreç, #18)
//	dump        idea card'ları JSON olarak stdout'a dök
//	scrub-quotes geriye dönük küfür/ağır hakaret temizliği (elle, #100)
//	lens-ab     altın set üzerinde v1/v3 mercek A/B karşılaştırması (elle, #165) — DB'ye yazmaz
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/musaay/idealode/backend/internal/api"
	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/pipeline"
	"github.com/musaay/idealode/backend/internal/store"
)

const usageText = `idealode — çok kaynaklı yazılım fikri öneri pipeline'ı

Kullanım: idealode <komut>

Komutlar:
  ingest      aktif kaynaklardan yeni post'ları çek (raw_posts)
  analyze     ön-filtre + LLM classification (post_analysis)
  synthesize  tema gruplama + idea synthesis (themes, ideas)
  seeds       pazar tohumlarını 3 mercekten geçir (market_derived kart)
  generate    kullanıcı bazlı ai_generated üretim (Faz 2)
  run         ingest -> analyze -> synthesize -> fuse -> seeds sırayla çalıştırır
  retheme     eski tip temalardaki kartsız gönderileri temalarından çözer (elle tetiklenir, #136)
                --limit N zorunlu, --dry-run isteğe bağlı
  api         JSON API'yi sunar (DATABASE_URL'i gören TEK süreç); PORT, varsayılan 8080
                (web arayüzü artık ayrı modül: ui/cmd/web, "serve" komutu orada)
  dump        idea card'ları JSON olarak stdout'a döker
  migrate     embed edilmiş .sql dosyalarını DB'ye uygular (elle tetiklenir)
  scrub-quotes geriye dönük küfür/ağır hakaret temizliği (elle tetiklenir, #100)
                --dry-run isteğe bağlı, hiçbir şey yazmaz
  lens-ab     altın set üzerinde v1/v3 mercek A/B karşılaştırması (elle tetiklenir, #165)
                DB'ye/eliminations'a hiçbir şey yazmaz, yalnız okur
                --set <json> zorunlu, --prompt v1|v3 zorunlu
                --lens <ad|all> (varsayılan all), --runs N (varsayılan 1)
                --budget-tokens N (varsayılan 0 = sınırsız), --out <csv> (varsayılan stdout)

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
	case "ingest", "analyze", "synthesize", "seeds", "generate", "fuse", "run", "retheme", "api", "dump", "migrate", "scrub-quotes", "lens-ab":
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
		runStart := time.Now() // eleme özeti (#138) bu koşunun kayıtlarını buradan filtreler
		// Token ölçümü (#144): koşu başında meter oluşturulur, ctx'e eklenir —
		// tüm alt cmd'ler bu ctx'i kullandığından aşama etiketleriyle
		// (llm.WithStage, çağrı noktalarında) eşleşen usage buraya birikir.
		usageMeter := llm.NewUsageMeter()
		ctx = llm.WithMeter(ctx, usageMeter)
		if err := cfg.RequireDatabaseURL(); err != nil {
			return err
		}
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
		logPendingIdeas(ctx, lockSt)
		logEliminationSummary(ctx, lockSt, runStart)
		logUsageSummary(usageMeter)
		return nil
	case "fuse":
		return cmdFuse(ctx, cfg)
	case "retheme":
		return cmdRetheme(ctx, cfg)
	case "api":
		return cmdAPI(ctx, cfg)
	case "dump":
		return cmdDump(ctx, cfg)
	case "migrate":
		return cmdMigrate(ctx, cfg)
	case "scrub-quotes":
		return cmdScrubQuotes(ctx, cfg)
	case "lens-ab":
		return cmdLensAB(ctx, cfg)
	}
	return fmt.Errorf("bilinmeyen komut: %q", cmd)
}

// logPendingIdeas, `run` sonunda moderasyon kuyruğu özetini loglar (#102) —
// lead PO onayını (published_at) buradan takip eder. Sorgu hatası koşuyu
// düşürmez, yalnız loglanır (özet bilgi, kritik değil).
func logPendingIdeas(ctx context.Context, st *store.Store) {
	pending, err := st.PendingIdeas(ctx)
	if err != nil {
		log.Printf("run: beklemede kart sorgusu HATA: %v", err)
		return
	}
	entries := make([]string, len(pending))
	for i, p := range pending {
		entries[i] = pendingIdeaSummary(p)
	}
	log.Printf("run: beklemede %d kart: %s", len(pending), strings.Join(entries, "; "))
}

// pendingIdeaSummary, `run: beklemede …` özetindeki tek kart satırını üretir
// (#108): id + başlık (60 karaktere kırpılır) + özgünlük kararı — fail ise
// kriter koduyla birlikte (`[fail K3]`), değilse yalnız karar (`[pass]`),
// mercek hiç çalışmadıysa (alanlar NULL) `[?]`. Veri-erişimi kararı (#131)
// AYRI bir `[veri erişimi: ...]` etiketiyle eklenir — yalnız dolu olduğunda
// (NULL ise hiç eklenmez: hem organik hem tohum yolu bu alanı artık
// doldurur, NULL yalnız mercek çağrısı hata verdiğinde ya da kart
// ai_blended olduğunda kalır — o durumlarda gürültülü bir `[veri erişimi:
// ?]` eklemek yerine etiket hiç görünmez; "fail" hiçbir yolda karta hiç
// ulaşmadığından burada yalnız pass/unsure görülür).
func pendingIdeaSummary(p store.PendingIdea) string {
	title := truncateRunes(p.Title, 60)
	tag := "?"
	if p.DistinctivenessVerdict != nil {
		tag = *p.DistinctivenessVerdict
		if tag == "fail" && p.DistinctivenessCriterion != nil {
			tag = fmt.Sprintf("fail %s", *p.DistinctivenessCriterion)
		}
	}
	summary := fmt.Sprintf("%d %q [%s]", p.ID, title, tag)
	if p.DataAccessVerdict != nil {
		summary += fmt.Sprintf(" [veri erişimi: %s]", *p.DataAccessVerdict)
	}
	return summary
}

// truncateRunes, s'yi en fazla n RUNE'a kırpar (Türkçe çok baytlı karakterler
// ortadan kesilmesin diye byte değil rune sayılır) ve kırpıldıysa "…" ekler.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// eliminationStageOrder, eleme özeti log satırındaki stage sırası — pipeline
// akışındaki doğal sıraya uyar (tema grupla → mercek → doygunluk → vendor →
// ödeme kapısı, #138). Haritada olmayan (bilinmeyen) stage'ler bu sıranın
// dışında, alfabetik olarak sona eklenir (savunmacı, bkz. eliminationSummaryLine).
var eliminationStageOrder = []string{
	"incoherent_theme",
	"blocking_lens",
	"distinctiveness",
	"vendor_internal",
	"payment_gate",
}

// eliminationStageDesc, eliminations.stage değerlerinin TR açıklaması — tek
// yerde sabit (#138, distinctivenessCriteriaDesc'teki [seeds.go] desenle
// aynı). Haritada olmayan stage ham adıyla loglanır (savunmacı).
var eliminationStageDesc = map[string]string{
	"incoherent_theme": "tutarsız tema",
	"blocking_lens":    "mercek",
	"distinctiveness":  "doygunluk",
	"vendor_internal":  "vendor",
	"payment_gate":     "ödeme kapısı",
}

// eliminationSummaryLine, stage->sayı haritasından `run` sonu özet log
// satırını üretir (#138): bilinen stage'ler eliminationStageOrder sırasına
// göre TR açıklamasıyla, bilinmeyenler ham adıyla alfabetik sırada sona
// eklenir. Toplam sıfırsa "" döner — çağıran bu durumda hiçbir şey basmaz
// (gürültü olmasın).
func eliminationSummaryLine(counts map[string]int) string {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return ""
	}

	seen := make(map[string]bool, len(counts))
	parts := make([]string, 0, len(counts))
	for _, stage := range eliminationStageOrder {
		if n, ok := counts[stage]; ok {
			parts = append(parts, fmt.Sprintf("%s: %d", eliminationStageDesc[stage], n))
			seen[stage] = true
		}
	}
	extra := make([]string, 0, len(counts)-len(seen))
	for stage := range counts {
		if !seen[stage] {
			extra = append(extra, stage)
		}
	}
	sort.Strings(extra)
	for _, stage := range extra {
		parts = append(parts, fmt.Sprintf("%s: %d", stage, counts[stage]))
	}

	return fmt.Sprintf("run: bu koşuda %d elendi (%s)", total, strings.Join(parts, ", "))
}

// logEliminationSummary, `run` sonunda bu koşuda (since'den itibaren) yazılan
// eleme kayıtlarının stage bazlı özetini tek satır TR log olarak yazar
// (#138). Sayı sıfırsa hiçbir şey basmaz. Sorgu hata verirse koşu DURMAZ,
// tek satır TR hata logu düşer (özet bilgi, kritik değil — logPendingIdeas
// deseniyle aynı).
func logEliminationSummary(ctx context.Context, st *store.Store, since time.Time) {
	counts, err := st.EliminationCountsSince(ctx, since)
	if err != nil {
		log.Printf("run: eleme özeti sorgusu HATA: %v", err)
		return
	}
	if line := eliminationSummaryLine(counts); line != "" {
		log.Print(line)
	}
}

// usageSummaryLine, meter'ın anlık görüntüsünden `run` sonu token özet
// satırını üretir (#144): aşamalar token'a göre büyükten küçüğe sıralanır
// (eşitlikte aşama adı alfabetik — belirli/tekrarlanabilir sıra), sayılar
// binlik ayraçlı (nokta) yazılır. Toplam token 0 ise (tüm çağrılar hatayla
// bitti ya da hiç çağrı yapılmadı) "" döner — çağıran bu durumda hiçbir şey
// basmaz (logEliminationSummary'nin "gürültü olmasın" ilkesiyle aynı).
func usageSummaryLine(snapshot map[string]llm.StageUsage) string {
	total := 0
	for _, u := range snapshot {
		total += u.TotalTokens
	}
	if total == 0 {
		return ""
	}

	stages := make([]string, 0, len(snapshot))
	for stage := range snapshot {
		stages = append(stages, stage)
	}
	sort.Slice(stages, func(i, j int) bool {
		a, b := snapshot[stages[i]], snapshot[stages[j]]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		return stages[i] < stages[j]
	})

	parts := make([]string, len(stages))
	for i, stage := range stages {
		u := snapshot[stage]
		parts[i] = fmt.Sprintf("%s %s (%d çağrı)", stage, formatThousands(u.TotalTokens), u.Calls)
	}
	return fmt.Sprintf("run: token kullanımı — toplam %s · %s", formatThousands(total), strings.Join(parts, " · "))
}

// logUsageSummary, `run` sonunda meter'daki aşama bazlı token kullanımını
// tek satır TR log olarak yazar (#144).
func logUsageSummary(m *llm.UsageMeter) {
	if line := usageSummaryLine(m.Snapshot()); line != "" {
		log.Print(line)
	}
}

// formatThousands, tam sayıyı TR biçiminde binlik ayraçlı (nokta) string'e
// çevirir (örn. 142310 -> "142.310").
func formatThousands(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// newChat, cfg'deki LLM ayarlarından (env ile seçilir, #96) canlı istemci
// kurar. Tüm LLM kullanan subcommand'lar bu tek yardımcıyı paylaşır.
func newChat(cfg *config.Config) llm.Chat {
	return llm.NewOpenAICompat(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
}

// newDistinctChat, özgünlük merceği için AYRI istemci kurar (#166, PO
// kararı 2026-09-22: Gemini 3.5 Flash Lite'a taşıma — yalnız bu mercek).
// cfg.UseDistinctivenessLLM() false ise (DISTINCTIVENESS_LLM_* üçü de dolu
// değilse) nil döner; çağıran bunu SynthesizeIdeas/ProcessSeeds'in opsiyonel
// distinctChat parametresine AYNEN geçirir — nil geçince özgünlük de
// varsayılan istemciyi kullanır (bugünkü davranış birebir).
func newDistinctChat(cfg *config.Config) llm.Chat {
	if !cfg.UseDistinctivenessLLM() {
		return nil
	}
	return llm.NewOpenAICompat(cfg.DistinctivenessLLMBaseURL, cfg.DistinctivenessLLMAPIKey, cfg.DistinctivenessLLMModel)
}

// logDistinctivenessLens, koşu başında özgünlük merceğinin hangi model/host
// üzerinden çalışacağını loglar (#166) — cmdSynthesize/cmdSeeds başında bir
// kez çağrılır (run komutu ikisini de sırayla çalıştırdığından iki kez
// görünebilir, zararsız).
func logDistinctivenessLens(cfg *config.Config) {
	model, base := cfg.LLMModel, cfg.LLMBaseURL
	if cfg.UseDistinctivenessLLM() {
		model, base = cfg.DistinctivenessLLMModel, cfg.DistinctivenessLLMBaseURL
	}
	log.Printf("özgünlük merceği: %s (%s)", model, hostOf(base))
}

// hostOf, log satırlarında API anahtarı/yol sızdırmadan sağlayıcıyı
// belirtmek için base URL'in host kısmını döner (llm.OpenAICompatClient'ın
// iç host() yardımcısıyla AYNI ilke — burada tekrarlanır çünkü o unexported).
func hostOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// cmdMigrate, embed edilmiş .sql dosyalarını DB'ye elle tetiklenerek uygular
// (bkz. internal/store/migrate.go). Otomatik/örtük çalışmaz.
func cmdMigrate(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	if err := store.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.Printf("migrate tamam")
	return nil
}

// Aşağıdaki komutlar sonraki issue'larda doldurulur (Faz 0 sırası: #2 şema,
// #3-4 connector'lar, #5-6 analyze, #7-8 synthesize, #9 run/dump).

func cmdIngest(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
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
	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)
	n, err := pipeline.Analyze(llm.WithStage(ctx, "analiz"), cfg, st, chat)
	log.Printf("analyze tamam: %d post işlendi", n)
	return err
}

// cmdFuse, market_derived kartlara yerel talep kanıtı eşleştirir (#43).
func cmdFuse(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)
	n, err := pipeline.FuseEvidence(llm.WithStage(ctx, "fuse"), cfg, st, chat)
	if err != nil {
		return err
	}
	log.Printf("fuse tamam: %d kart işlendi", n)
	return nil
}

// cmdRetheme, eski tip temalardaki (theme_name == domain_tag) kartsız
// gönderileri temalarından çözer (#136) — elle tetiklenir, cron/run akışına
// eklenmez. LLM kullanmaz, yeniden kümeleme yapmaz: çözülen gönderiler bir
// sonraki `synthesize`/`run` çağrısında GroupThemes tarafından yeniden
// kümelenir.
func cmdRetheme(ctx context.Context, cfg *config.Config) error {
	fs := flag.NewFlagSet("retheme", flag.ExitOnError)
	limit := fs.Int("limit", 0, "en fazla çözülecek gönderi sayısı (zorunlu, > 0)")
	dryRun := fs.Bool("dry-run", false, "hiçbir şey yazma, yalnız hedef kümeyi raporla")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *limit <= 0 {
		return fmt.Errorf("--limit zorunlu ve > 0 olmalı")
	}

	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	if *dryRun {
		return pipeline.RethemeDryRun(ctx, st, cfg.MinThemeEvidence, *limit)
	}

	result, err := pipeline.Retheme(ctx, st, cfg.MinThemeEvidence, *limit)
	if err != nil {
		return err
	}
	log.Printf("retheme: %d gönderi %d eski temadan çözüldü, kalan %d", result.Resolved, result.Themes, result.Remaining)
	return nil
}

// cmdScrubQuotes, TÜM kartların example_quotes/local_evidence alanlarını
// profanity.Filter'dan geçirip küfür/ağır hakaret içeren satırları geriye
// dönük ATAR (#100) — elle tetiklenir, cron/run akışına eklenmez. Yeni
// kartlar zaten store yazım sınırında (InsertIdea vb.) korunuyor; bu komut
// yalnız o guard'tan ÖNCE yazılmış eski kartlar için.
func cmdScrubQuotes(ctx context.Context, cfg *config.Config) error {
	fs := flag.NewFlagSet("scrub-quotes", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "hiçbir şey yazma, yalnız düşecek satırları raporla")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	_, err = pipeline.ScrubQuotes(ctx, st, *dryRun)
	return err
}

// cmdLensAB, altın set üzerinde v1/v3 mercek A/B karşılaştırmasını çalıştırır
// (#165, üst plan #163 §5/§6.2). DB'ye/eliminations'a HİÇBİR ŞEY yazmaz —
// yalnız GetIdeaForAudit/GetElimination ile okur (bkz. pipeline.RunLensAB).
// v3 metinleri lens_prompts_v3.go'da ayrı sabitler — bu komut dışında
// hiçbir yere bağlı DEĞİL.
func cmdLensAB(ctx context.Context, cfg *config.Config) error {
	fs := flag.NewFlagSet("lens-ab", flag.ExitOnError)
	setPath := fs.String("set", "", "altın set JSON dosyası (zorunlu)")
	lens := fs.String("lens", "all", "mercek adı (third_party|data_access|market_viability|distinctiveness) ya da all")
	promptVersion := fs.String("prompt", "", "v1|v3 (zorunlu)")
	runs := fs.Int("runs", 1, "her çift için koşu sayısı (tekrarlanabilirlik için >=2)")
	budgetTokens := fs.Int("budget-tokens", 0, "birikimli token bütçesi (0 = sınırsız)")
	outPath := fs.String("out", "", "CSV çıktı dosyası (boşsa stdout)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if strings.TrimSpace(*setPath) == "" {
		return fmt.Errorf("--set zorunlu")
	}
	if *promptVersion != "v1" && *promptVersion != "v3" {
		return fmt.Errorf("--prompt v1|v3 olmalı")
	}

	raw, err := os.ReadFile(*setPath)
	if err != nil {
		return fmt.Errorf("altın set okunamadı: %w", err)
	}
	var set []pipeline.GoldenCase
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("altın set JSON değil: %w", err)
	}

	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)
	result, err := pipeline.RunLensAB(ctx, st, chat, set, pipeline.LensABOptions{
		Lens: *lens, PromptVersion: *promptVersion, Runs: *runs, BudgetTokens: *budgetTokens,
	})
	if err != nil {
		return err
	}

	var out io.Writer = os.Stdout
	if strings.TrimSpace(*outPath) != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	if err := pipeline.WriteLensABCSV(out, result.Rows); err != nil {
		return err
	}

	fmt.Fprint(os.Stderr, pipeline.FormatLensABSummary(result))
	return nil
}

func cmdSynthesize(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)
	distinctChat := newDistinctChat(cfg)
	logDistinctivenessLens(cfg)
	if _, err := pipeline.GroupThemes(llm.WithStage(ctx, "kümeleme"), st, chat); err != nil {
		return fmt.Errorf("tema gruplama: %w", err)
	}
	n, err := pipeline.SynthesizeIdeas(ctx, cfg, st, chat, distinctChat)
	log.Printf("synthesize tamam: %d yeni idea", n)
	return err
}

// cmdSeeds, elle küratörlüğü yapılan pazar tohumlarını (radar-seeds.jsonl)
// 3 mercekten geçirip market_derived kart üretir (#56).
func cmdSeeds(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)
	distinctChat := newDistinctChat(cfg)
	logDistinctivenessLens(cfg)
	n, err := pipeline.ProcessSeeds(ctx, cfg, st, chat, pipeline.RadarSeedsJSONL, distinctChat)
	if err != nil {
		return err
	}
	log.Printf("seeds tamam: %d yeni idea", n)
	return nil
}

// cmdAPI, JSON API sunucusunu ayağa kaldırır (#18). DATABASE_URL'i gören TEK
// süreçtir; `serve` (web) buraya HTTP ile bağlanır. Public domain almaz —
// yalnız Railway iç ağında (idealode-web) erişilir. Kart sohbeti/blend
// (#66) LLM'e yalnız BU süreçten gider — RequireLLM bu yüzden zorunlu.
func cmdAPI(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireLLM(); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	chat := newChat(cfg)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return api.NewServer(st, chat).ListenAndServe(ctx, ":"+port)
}

func cmdGenerate(ctx context.Context, cfg *config.Config) error {
	return fmt.Errorf("generate Faz 2 kapsamında (bkz. issue #20)")
}

func cmdDump(ctx context.Context, cfg *config.Config) error {
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
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
