package pipeline

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/connector"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// RadarSeedsJSONL, elle küratörlüğü yapılan "doğrulanmış pazar tohumları"
// listesidir (#56): her satır gelir/traction kanıtı olan mevcut bir ürün/
// trend (kind="revenue", varsayılan) YA DA GitHub Trending'de kalıcılık
// gösteren bir repo (kind="trending", #89 — SIKI kapı + ayrı 4. mercek).
// ProcessSeeds ilkini market_derived, ikincisini momentum_derived kart
// yapar. Dosya JSONL — satır başına bir JSON nesnesi, yorum satırı YOK.
//
//go:embed seeds/radar-seeds.jsonl
var RadarSeedsJSONL string

// radarSeed, seeds/radar-seeds.jsonl'deki tek satırın şeması.
type radarSeed struct {
	Date      string `json:"date"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Evidence  string `json:"evidence"`
	SourceURL string `json:"source_url"`
	TRAngle   string `json:"tr_angle"`
	// Kind: "revenue" (varsayılan, mevcut satırlarda alan yok → geriye
	// uyumlu) ya da "trending" (#89 — GitHub Trending kalıcılık tohumu,
	// SIKI kapıdan geçip momentum_derived kart üretir).
	Kind string `json:"kind"`
}

// parseRadarSeeds, JSONL'i savunmacı ayrıştırır: bozuk/eksik satır loglanır
// ve atlanır, pipeline'ı düşürmez.
func parseRadarSeeds(jsonl string) []radarSeed {
	var out []radarSeed
	for i, line := range strings.Split(jsonl, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s radarSeed
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			log.Printf("seeds: satır %d bozuk JSON, atlandı: %v", i+1, err)
			continue
		}
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.SourceURL) == "" {
			log.Printf("seeds: satır %d name/source_url eksik, atlandı", i+1)
			continue
		}
		if strings.TrimSpace(s.Kind) == "" {
			s.Kind = "revenue"
		}
		out = append(out, s)
	}
	return out
}

// Mercek (lens) sistem prompt'ları — üçü de "pass" değilse tohum elenir,
// kart üretilmez. Şema synthesize.go'daki savunmacı VERDICT parse desenini
// izler.
const lensThirdPartySystem = `You evaluate whether a proposed software product idea, based on a validated market seed (an existing successful product or trend), could be BUILT BY AN INDEPENDENT THIRD-PARTY developer — not merely patched by the original vendor.

FAIL if the underlying opportunity is actually a defect, bug, or feature gap that only the ORIGINAL vendor could reasonably fix (their own onboarding, their own pricing, their own outage). PASS if an independent developer could build a STANDALONE product serving the same or an adjacent need, without needing to be the original vendor.

Return ONLY a JSON object: {"verdict":"pass|fail|unsure","reason":"..."}`

const lensDataAccessSystem = `You evaluate the DATA-ACCESS feasibility of a proposed software product idea for an independent third-party developer.

FAIL if the idea's core function requires closed, proprietary data or private APIs that incumbent vendors control and have no incentive to open (e.g. a food-delivery app's live order feed, a closed marketplace's internal inventory).
PASS if the idea can be built on open/regulated interfaces: public APIs, open banking, RSS, email parsing, user-provided/exportable data, or similar.

Return ONLY a JSON object: {"verdict":"pass|fail|unsure","reason":"..."}`

const lensMarketViabilitySystem = `You evaluate whether a proposed software product idea has REALISTIC monetization potential, either in Turkey (TR) or globally.

PASS if there is a plausible path to real revenue: a market of paying users/businesses exists, similar products already charge for this, or the pain is acute enough that people would pay.
FAIL if the idea has no realistic path to revenue (e.g. a tiny hobbyist niche, a free-only expectation, or a need already fully solved for free).

Return ONLY a JSON object: {"verdict":"pass|fail|unsure","reason":"..."}`

// lensProductizableSystem: ivme tohumlarına özgü 4. mercek (#89 kapı madde
// 4) — awesome-list, eğitim/kurs, makale/paper, model ağırlığı, saf
// kütüphane/framework gibi son-kullanıcıya doğrudan ürün olmayan repoları
// eler. Yalnız kind=="trending" tohumlarda mevcut 3 mercekle birlikte koşar.
const lensProductizableSystem = `You evaluate whether a proposed software product idea, based on a trending GitHub repository, is PRODUCTIZABLE as an end-user tool or application — something a non-contributor user could actually install/open and use.

FAIL if the underlying repository is an awesome-list/curated-links collection, a tutorial or course, a research paper or writeup, a set of model weights/checkpoints, or a pure library/framework meant to be consumed by other developers rather than used directly by an end user.
PASS if the repository is (or clearly evolves into) a standalone end-user tool or application.

Return ONLY a JSON object: {"verdict":"pass|fail|unsure","reason":"..."}`

// seedLens, tek bir mercek: adı (log/rapor için Türkçe) + sistem prompt'u.
type seedLens struct {
	name   string
	system string
}

// seedLenses, üç merceğin adı+sistem prompt'u (log/rapor için Türkçe ad).
// kind=="revenue" tohumlarda TEK BAŞINA, kind=="trending" tohumlarda
// lensProductizableSystem'in ÖNÜNE eklenmiş biçimde kullanılır (bkz.
// trendingLenses).
var seedLenses = []seedLens{
	{"üçüncü-taraf inşa edilebilirlik", lensThirdPartySystem},
	{"veri-erişimi", lensDataAccessSystem},
	{"pazar-işlerliği", lensMarketViabilitySystem},
}

// trendingLenses, ivme tohumlarının koştuğu tüm mercekler: 4. mercek
// (ürünleştirilebilirlik) + mevcut 3 mercek AYNEN (#89 kapı madde 4-5).
var trendingLenses = append([]seedLens{{"ürünleştirilebilirlik", lensProductizableSystem}}, seedLenses...)

type lensVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// parseLensVerdict, mercek cevabını savunmacı ayrıştırır: JSON değilse ya da
// verdict tanınmıyorsa "unsure" sayılır (pass DEĞİL) — belirsizlikte kart
// üretilmez.
func parseLensVerdict(raw string) lensVerdict {
	var v lensVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return lensVerdict{Verdict: "unsure", Reason: fmt.Sprintf("LLM yanıtı JSON değil: %.120s", raw)}
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	if v.Verdict != "pass" && v.Verdict != "fail" && v.Verdict != "unsure" {
		v.Verdict = "unsure"
	}
	return v
}

func lensUserPrompt(s radarSeed) string {
	return fmt.Sprintf("Seed name: %s\nSummary: %s\nEvidence: %s\nTR angle: %s",
		s.Name, s.Summary, s.Evidence, s.TRAngle)
}

// trendingGateStore, ivme tohumu kapısının (#89) ihtiyaç duyduğu store
// operasyonlarını soyutlar — canlı DB olmadan fake ile test edilebilsin
// diye (bkz. seeds_test.go). *store.Store bu arayüzü zaten sağlar.
type trendingGateStore interface {
	CountIdeasSince(ctx context.Context, sourceType string, sinceDays int) (int, error)
	TrendingPersistenceDays(ctx context.Context, repo string, sinceDays int) (int, int, error)
}

// trendingMetaFetcher, GitHub repo meta çekimini soyutlar — testte httptest
// sunucusuna bağlı bir fake ile değiştirilir; canlıda connector.FetchRepoMeta
// kullanılır.
type trendingMetaFetcher func(ctx context.Context, owner, repo string) (connector.RepoMeta, error)

// fetchTrendingRepoMeta, ProcessSeeds'in kullandığı meta fetcher — canlıda
// connector.FetchRepoMeta'ya sabitlenir; testler (seeds_trending_test.go)
// httptest sunucusuna bağlı bir sahteyle geçici olarak değiştirir (ProcessSeeds'in
// public imzasını değiştirmemek için paket seviyesinde değişken).
var fetchTrendingRepoMeta trendingMetaFetcher = connector.FetchRepoMeta

// trendingWeeklyCapDays, madde 6'nın (sert tavan) ölçüm penceresidir.
const trendingWeeklyCapDays = 7

// trendingPersistenceWindowDays, madde 1'in (kalıcılık) ölçüm penceresidir.
const trendingPersistenceWindowDays = 14

// trendingMinPersistenceDays: son 14 günde en az bu kadar FARKLI günde
// görünmemiş repo elenir (tek günlük haber patlamaları elenir).
const trendingMinPersistenceDays = 3

// trendingMaxAge: repo bundan daha eskiyse elenir (erken sinyal şartı).
const trendingMaxAge = 366 * 24 * time.Hour

// trendingMinUsageRatio: forks/stars bu oranın altındaysa VE open_issues
// trendingMinOpenIssues'un altındaysa elenir (gerçek kullanım şartı).
const trendingMinUsageRatio = 0.05
const trendingMinOpenIssues = 20

// trendingGateAction, evaluateTrendingGate'in kararını taşır.
type trendingGateAction int

const (
	trendingGatePass   trendingGateAction = iota // a-d geçti, mercek+kart üretimine devam
	trendingGateReject                           // elendi — imleç YAZILIR (bir daha denenmez)
	trendingGateRetry                            // atlandı — imleç YAZILMAZ (sonraki koşuda yeniden denenir)
)

// trendingGateResult, evaluateTrendingGate'in çıktısı.
type trendingGateResult struct {
	action        trendingGateAction
	reason        string
	repo          string // "owner/repo"
	stars         int
	lastDelta     int // günlük ★ artışı (son görülen gün)
	persistedDays int // son 14 günde kaç farklı gün görüldü
}

// parseOwnerRepo, bir GitHub repo URL'sinden "owner/repo" çıkarır.
// "https://github.com/owner/repo" ve sondaki "/" varyantlarını kabul eder.
func parseOwnerRepo(sourceURL string) (string, bool) {
	u := strings.TrimSuffix(strings.TrimSpace(sourceURL), "/")
	idx := strings.Index(u, "github.com/")
	if idx < 0 {
		return "", false
	}
	u = u[idx+len("github.com/"):]
	parts := strings.SplitN(u, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// evaluateTrendingGate, ivme kapısının kodla ölçülen maddelerini uygular
// (#89 kapı madde 6, 1, 2, 3 — bu sıra maliyet artan sırada: önce ucuz DB
// sorguları, sonra GitHub API çağrısı; mercekler (madde 4-5) ve kart
// üretimi (madde 2-3'ün geçtiği tohumlar için) ProcessSeeds'te devam eder).
func evaluateTrendingGate(ctx context.Context, st trendingGateStore, fetchMeta trendingMetaFetcher, seed radarSeed) trendingGateResult {
	// Madde 6: haftalık sert tavan.
	n, err := st.CountIdeasSince(ctx, "momentum_derived", trendingWeeklyCapDays)
	if err != nil {
		return trendingGateResult{action: trendingGateRetry, reason: fmt.Sprintf("haftalık tavan sorgusu HATA: %v", err)}
	}
	if n >= 1 {
		return trendingGateResult{action: trendingGateRetry, reason: "ivme tavanı dolu (son 7 günde zaten 1 momentum_derived kart açıldı), tohum sonraki haftaya"}
	}

	repo, ok := parseOwnerRepo(seed.SourceURL)
	if !ok {
		return trendingGateResult{action: trendingGateReject, reason: fmt.Sprintf("source_url'den owner/repo çıkarılamadı: %q", seed.SourceURL)}
	}

	// Madde 1: kalıcılık.
	days, lastDelta, err := st.TrendingPersistenceDays(ctx, repo, trendingPersistenceWindowDays)
	if err != nil {
		return trendingGateResult{action: trendingGateRetry, reason: fmt.Sprintf("kalıcılık sorgusu HATA: %v", err)}
	}
	if days < trendingMinPersistenceDays {
		return trendingGateResult{action: trendingGateReject, reason: fmt.Sprintf("kalıcılık yetersiz: son %d günde %d gün (≥%d gerekli)", trendingPersistenceWindowDays, days, trendingMinPersistenceDays)}
	}

	// Madde 2-3: GitHub API meta (yaş + gerçek kullanım).
	owner, name, _ := strings.Cut(repo, "/")
	meta, err := fetchMeta(ctx, owner, name)
	if err != nil {
		if errors.Is(err, connector.ErrRepoNotFound) {
			return trendingGateResult{action: trendingGateReject, reason: "repo bulunamadı (404)"}
		}
		if errors.Is(err, connector.ErrGitHubRateLimited) {
			return trendingGateResult{action: trendingGateRetry, reason: "GitHub API oran sınırı (403/429), sonraki koşuda yeniden denenecek"}
		}
		return trendingGateResult{action: trendingGateRetry, reason: fmt.Sprintf("GitHub API meta HATA: %v", err)}
	}

	if time.Since(meta.CreatedAt) > trendingMaxAge {
		return trendingGateResult{action: trendingGateReject, reason: fmt.Sprintf("repo yaşı >12 ay (oluşturulma: %s)", meta.CreatedAt.Format("2006-01-02"))}
	}

	usageOK := meta.OpenIssuesCount >= trendingMinOpenIssues
	if !usageOK && meta.StargazersCount > 0 {
		usageOK = float64(meta.ForksCount)/float64(meta.StargazersCount) >= trendingMinUsageRatio
	}
	if !usageOK {
		return trendingGateResult{action: trendingGateReject, reason: fmt.Sprintf("gerçek kullanım yetersiz: forks=%d stars=%d open_issues=%d", meta.ForksCount, meta.StargazersCount, meta.OpenIssuesCount)}
	}

	return trendingGateResult{
		action:        trendingGatePass,
		repo:          repo,
		stars:         meta.StargazersCount,
		lastDelta:     lastDelta,
		persistedDays: days,
	}
}

// momentumEvidenceLine, momentum_derived kartın example_quotes/kanıt
// metnidir — LLM ÜRETMEZ, koddan hesaplanan sayılarla yazılır (kanıt
// tahrif edilmez ilkesi).
func momentumEvidenceLine(stars, lastDelta, persistedDays int) string {
	return fmt.Sprintf("★%d toplam, +%d/gün (GitHub trending, son 14 günde %d gün listede) — gelir kanıtı yok",
		stars, lastDelta, persistedDays)
}

// seedCardSystemTmpl: gelir kanıtlı bir emsalden + TR açısından market_derived
// kart üretir. synthesizeSystemTmpl'den kasıtlı olarak farklı ve daha kısadır
// — burada üçüncü-taraf/veri-erişimi elemesi zaten 3 mercekten geçti, LLM'e
// yeniden skip kararı verdirilmez.
const seedCardSystemTmpl = `You generate a concrete software product idea card ("market_derived") from a validated market seed — an existing product or trend with real revenue/traction evidence — adapted with a realistic localization angle.

CRITICAL CONSTRAINT: Propose a realistic, grounded, SOFTWARE-heavy idea that an experienced developer (using AI-assisted coding tools) could build and deploy alone or with a tiny team within a few weeks to a few months.

Return ONLY a JSON object:
{"title":"...","problem_statement":"...","proposed_solution":"...","target_user":"...","urgency_score":1-5,"monetization_signal":0-5,"known_competitors_ai_guess":"...","domain_tags":["slug"]}

Rules:
- title, problem_statement, proposed_solution, target_user MUST be written in %s.
- domain_tags: 1-5 canonical ENGLISH slugs (lowercase, dash-separated). Never translate tags.
- urgency_score: integer 1-5.
- monetization_signal: integer 0-5 (this idea already has revenue evidence from the seed — score accordingly, usually 3-5).
- known_competitors_ai_guess: short informational note, phrased as an unverified AI guess; empty string if you have none.`

func seedCardUserPrompt(s radarSeed) string {
	return fmt.Sprintf("Seed name: %s\nSummary: %s\nRevenue/traction evidence: %s\nSource: %s\nTR angle / localization idea: %s",
		s.Name, s.Summary, s.Evidence, s.SourceURL, s.TRAngle)
}

// seedMomentumCardSystemTmpl: seedCardSystemTmpl'in ivme varyantı (#89) —
// gelir kanıtı YOKTUR, yalnız GitHub Trending kalıcılık/kullanım kanıtı
// vardır; monetization_signal buna göre düşük tutulur.
const seedMomentumCardSystemTmpl = `You generate a concrete software product idea card ("momentum_derived") from a validated GROWTH SEED — a GitHub repository showing real trending momentum (stars, forks, persistence on GitHub Trending) but NO revenue evidence — adapted with a realistic localization angle.

CRITICAL CONSTRAINT: Propose a realistic, grounded, SOFTWARE-heavy idea that an experienced developer (using AI-assisted coding tools) could build and deploy alone or with a tiny team within a few weeks to a few months.

Return ONLY a JSON object:
{"title":"...","problem_statement":"...","proposed_solution":"...","target_user":"...","urgency_score":1-5,"monetization_signal":0-5,"known_competitors_ai_guess":"...","domain_tags":["slug"]}

Rules:
- title, problem_statement, proposed_solution, target_user MUST be written in %s.
- domain_tags: 1-5 canonical ENGLISH slugs (lowercase, dash-separated). Never translate tags.
- urgency_score: integer 1-5.
- monetization_signal: integer 0-5 — there is NO revenue evidence for this seed, only growth/traction. Score 0-2 UNLESS the idea has a clear paid path (e.g. an obvious enterprise/pro tier, a paid API, a SaaS wrapper with real willingness-to-pay).
- known_competitors_ai_guess: short informational note, phrased as an unverified AI guess; empty string if you have none.`

func seedMomentumCardUserPrompt(s radarSeed, repo string, stars, lastDelta, persistedDays int) string {
	return fmt.Sprintf("Seed name: %s\nRepo: %s\nSummary: %s\nGrowth evidence: %s\nSource: %s\nTR angle / localization idea: %s",
		s.Name, repo, s.Summary, momentumEvidenceLine(stars, lastDelta, persistedDays), s.SourceURL, s.TRAngle)
}

// seedRawPost, tohumun raw_posts işlenme imlecidir: platform='radar_seed',
// source_ref=source_url (UNIQUE(platform, source_ref) üzerinden idempotent).
func seedRawPost(s radarSeed) store.RawPost {
	return store.RawPost{
		Platform:  "radar_seed",
		SourceRef: s.SourceURL,
		Community: "radar",
		Title:     s.Name,
		Body:      s.Summary + " | " + s.Evidence,
		URL:       s.SourceURL,
	}
}

// ProcessSeeds, elle küratörlüğü yapılan pazar tohumlarını (seedsJSONL) 3
// mercekten geçirir ve üçü de "pass" ise market_derived idea card üretir.
// Her tohum raw_posts'a platform='radar_seed' olarak yazılır — bu yazım hem
// idempotency kontrolü (InsertRawPosts ON CONFLICT DO NOTHING) hem de
// "işlendi" imlecidir: ikinci koşuda aynı tohum tekrar işlenmez, dolayısıyla
// analyze'ın hiç görmeyeceği bu satırlar LLM mercek/kart maliyetini de bir
// kereye indirger.
func ProcessSeeds(ctx context.Context, cfg *config.Config, st *store.Store, chat llm.Chat, seedsJSONL string) (int, error) {
	seeds := parseRadarSeeds(seedsJSONL)
	if len(seeds) == 0 {
		log.Printf("seeds: işlenecek tohum yok")
		return 0, nil
	}

	created := 0
	for i, seed := range seeds {
		if ctx.Err() != nil {
			return created, ctx.Err()
		}

		// "İşlenmiş mi" kontrolü mark yazımından AYRIDIR: geçici bir LLM
		// hatası (ağ/kota) tohumu kalıcı yakmasın — mark yalnız aşağıdaki
		// iki tanımlı sonuçta (kart üretildi/dup, ya da mercekler gerçek
		// bir fail verdict'i döndürdü) yazılır; LLM çağrı hatasında YAZILMAZ,
		// tohum sonraki koşuda yeniden denenir (advisory lock'lu tek koşu
		// olduğundan yarış riski yok).
		already, err := st.RawPostExists(ctx, "radar_seed", seed.SourceURL)
		if err != nil {
			return created, err
		}
		if already {
			continue
		}
		markProcessed := func() error {
			_, err := st.InsertRawPosts(ctx, []store.RawPost{seedRawPost(seed)})
			return err
		}

		// İvme tohumu (#89): kodla ölçülen kapı maddeleri (haftalık tavan,
		// kalıcılık, yaş, gerçek kullanım) mercek/kart LLM maliyetinden ÖNCE
		// uygulanır. "retry" imleç YAZMADAN atlar; "reject" imleç yazıp eler.
		isTrending := seed.Kind == "trending"
		var gate trendingGateResult
		if isTrending {
			gate = evaluateTrendingGate(ctx, st, fetchTrendingRepoMeta, seed)
			switch gate.action {
			case trendingGateRetry:
				log.Printf("seeds: %q ivme kapısı atlandı: %s", seed.Name, gate.reason)
				continue
			case trendingGateReject:
				if err := markProcessed(); err != nil {
					return created, err
				}
				log.Printf("seeds: %q ivme kapısında elendi: %s", seed.Name, gate.reason)
				continue
			}
		}

		lenses := seedLenses
		if isTrending {
			lenses = trendingLenses
		}

		verdicts := make([]lensVerdict, len(lenses))
		lensErr := false
		for li, lens := range lenses {
			raw, err := chat.ChatJSON(ctx, lens.system, lensUserPrompt(seed))
			if err != nil {
				log.Printf("seeds: %q mercek %q HATA: %v — tohum atlandı (yeniden denenecek)", seed.Name, lens.name, err)
				lensErr = true
				break
			}
			verdicts[li] = parseLensVerdict(raw)
		}
		if lensErr {
			continue
		}

		var failedNames, failedReasons []string
		allPass := true
		for li, v := range verdicts {
			if v.Verdict != "pass" {
				allPass = false
				failedNames = append(failedNames, lenses[li].name)
				failedReasons = append(failedReasons, v.Reason)
			}
		}
		if !allPass {
			if err := markProcessed(); err != nil {
				return created, err
			}
			log.Printf("seed %q elendi (mercek: %s — %s)",
				seed.Name, strings.Join(failedNames, ", "), strings.Join(failedReasons, "; "))
			continue
		}

		var system, userPrompt string
		if isTrending {
			system = fmt.Sprintf(seedMomentumCardSystemTmpl, langName(cfg.OutputLang))
			userPrompt = seedMomentumCardUserPrompt(seed, gate.repo, gate.stars, gate.lastDelta, gate.persistedDays)
		} else {
			system = fmt.Sprintf(seedCardSystemTmpl, langName(cfg.OutputLang))
			userPrompt = seedCardUserPrompt(seed)
		}
		raw, err := chat.ChatJSON(ctx, system, userPrompt)
		if err != nil {
			log.Printf("seeds: %q kart üretimi HATA: %v — atlandı (yeniden denenecek)", seed.Name, err)
			continue
		}
		idea, err := parseIdeaResponse(raw)
		if err != nil {
			log.Printf("seeds: %q kart cevabı HATA: %v — atlandı (yeniden denenecek)", seed.Name, err)
			continue
		}
		if isTrending {
			idea.SourceType = "momentum_derived"
			idea.EvidenceCount = 1
			idea.SourceThemeID = nil
			idea.ExampleQuotes = []string{momentumEvidenceLine(gate.stars, gate.lastDelta, gate.persistedDays)}
		} else {
			idea.SourceType = "market_derived"
			idea.EvidenceCount = 1
			idea.SourceThemeID = nil
			idea.ExampleQuotes = []string{
				fmt.Sprintf("Kanıt (%s): %s — %s", seed.Name, seed.Evidence, seed.SourceURL),
			}
		}

		dup, existing, err := findDuplicate(ctx, st, chat, idea)
		if err != nil {
			return created, err
		}
		if dup {
			// Tohumun bağlı olduğu bir tema yok — mükerrer kanıt
			// MergeIdeaEvidence ile eklenmez, sadece logla.
			if err := markProcessed(); err != nil {
				return created, err
			}
			log.Printf("seeds: %q mükerrer -> %q kartına denk geldi, kart açılmadı", seed.Name, existing.Title)
			continue
		}

		// Sıra önemli: ÖNCE InsertIdea (asıl değerli iş), SONRA markProcessed.
		// Tersi olsaydı InsertIdea hata verdiğinde tohum mark'lanmış ama
		// kartsız kalır (kalıcı kayıp). markProcessed InsertIdea'dan SONRA
		// başarısız olursa (örn. geçici DB hatası) kart zaten yazılmıştır;
		// logla ve devam et — bir sonraki koşuda findDuplicate zaten aynı
		// kartı yakalayacağından çift kart oluşmaz, yalnız gereksiz bir LLM
		// turu tekrarlanır.
		if _, err := st.InsertIdea(ctx, idea); err != nil {
			return created, err
		}
		if err := markProcessed(); err != nil {
			log.Printf("seeds: %q kart yazıldı ama mark HATA: %v — sonraki koşuda dedup yakalar", seed.Name, err)
		}
		created++
		log.Printf("seeds: idea üretildi: %q (tohum=%s)", idea.Title, seed.Name)

		if i < len(seeds)-1 {
			select {
			case <-time.After(time.Duration(cfg.LLMSleepMS) * time.Millisecond):
			case <-ctx.Done():
				return created, ctx.Err()
			}
		}
	}
	return created, nil
}
