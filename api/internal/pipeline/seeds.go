package pipeline

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// RadarSeedsJSONL, elle küratörlüğü yapılan "doğrulanmış pazar tohumları"
// listesidir (#56): her satır gelir/traction kanıtı olan mevcut bir ürün/
// trend. ProcessSeeds bunları 3 mercekten geçirip market_derived kart
// üretir. Dosya JSONL — satır başına bir JSON nesnesi, yorum satırı YOK.
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

// seedLenses, üç merceğin adı+sistem prompt'u (log/rapor için Türkçe ad).
var seedLenses = []struct {
	name   string
	system string
}{
	{"üçüncü-taraf inşa edilebilirlik", lensThirdPartySystem},
	{"veri-erişimi", lensDataAccessSystem},
	{"pazar-işlerliği", lensMarketViabilitySystem},
}

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

		verdicts := make([]lensVerdict, len(seedLenses))
		lensErr := false
		for li, lens := range seedLenses {
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
				failedNames = append(failedNames, seedLenses[li].name)
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

		system := fmt.Sprintf(seedCardSystemTmpl, langName(cfg.OutputLang))
		raw, err := chat.ChatJSON(ctx, system, seedCardUserPrompt(seed))
		if err != nil {
			log.Printf("seeds: %q kart üretimi HATA: %v — atlandı (yeniden denenecek)", seed.Name, err)
			continue
		}
		idea, err := parseIdeaResponse(raw)
		if err != nil {
			log.Printf("seeds: %q kart cevabı HATA: %v — atlandı (yeniden denenecek)", seed.Name, err)
			continue
		}
		idea.SourceType = "market_derived"
		idea.EvidenceCount = 1
		idea.SourceThemeID = nil
		idea.ExampleQuotes = []string{
			fmt.Sprintf("Kanıt (%s): %s — %s", seed.Name, seed.Evidence, seed.SourceURL),
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
