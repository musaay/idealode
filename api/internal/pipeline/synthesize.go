package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// Koşu başına en fazla bu kadar temadan idea üretilir ("sakin ilerleme").
const synthesizeThemeLimit = 10

// Tema başına prompt'a giren en fazla kanıt post'u.
const evidencePerTheme = 8

// Ortak prompt prensibi (plan madde 7): tek kişi / küçük ekiple, AI-destekli
// kodlamayla haftalar-aylar içinde inşa edilip deploy edilebilecek,
// YAZILIM-ağırlıklı, ayağı yere basan fikirler. Rekabet FİLTRE DEĞİL.
const synthesizeSystemTmpl = `You generate concrete software product ideas ("idea cards") from clustered evidence of real user pain points collected from developer/social platforms.

CRITICAL CONSTRAINT: Only propose realistic, grounded, SOFTWARE-heavy ideas that an experienced developer (using AI-assisted coding tools) could build and deploy alone or with a tiny team within a few weeks to a few months. AVOID ideas that require hardware manufacturing, physical infrastructure, large capital, heavy regulation, or a big team. The idea must be a SaaS, an API, a browser extension, an automation tool, an AI-wrapper or similar — something that can quickly be turned into an MVP.

Competition is NOT a filter: the existence of competitors VALIDATES demand. Never reject or water down an idea because "someone already built it". If you can guess likely competitors, list them as an informational note only.

THIRD-PARTY RULE (critical): Evidence about existing products comes in two kinds — treat them very differently:
(a) DEFECTS of a specific product: crashes, login failures, technical errors, bad support, that product's own pricing. Only that vendor can fix these; they are NOT valid ideas for an independent builder. If the evidence contains ONLY defects, return exactly {"skip": true, "reason": "vendor-internal"} and nothing else.
(b) UNMET NEEDS voiced as feature wishes: "I wish this app could X", "it would be great if it had Y", "why is there no way to Z". These are VALUABLE — they reveal a need nobody serves. Never propose patching the vendor's app; instead design a STANDALONE product an independent developer could build and sell that serves the underlying need, ideally across many similar apps, platforms or users.
(c) STRUCTURALLY-BOUND wishes: if the "unmet need" can only be solved by changing the vendor's own security policy, regulatory compliance flow, or core structural decision (e.g. a government portal's own login security, a platform's own return/refund enforcement) — even reframed as a wrapper/dashboard/aggregator, an independent builder cannot actually fix the underlying constraint. Treat this the same as (a): return exactly {"skip": true, "reason": "vendor-internal"} and nothing else.

DATA-ACCESS RULE: A standalone idea is only valid if an independent builder can actually reach the data or integration it depends on. If the core of the idea requires closed data or private APIs that incumbent vendors control and have no incentive to open (e.g. a food-delivery app's live order feed), do NOT produce that idea — return {"skip": true, "reason": "data-locked"}. Open/regulated interfaces (public APIs, open banking, RSS, email parsing, user-provided data) are fine.

Return ONLY a JSON object:
{"title":"...","problem_statement":"...","proposed_solution":"...","target_user":"...","example_quotes":["..."],"urgency_score":1-5,"monetization_signal":0-5,"known_competitors_ai_guess":"...","domain_tags":["slug"]}

Rules:
- title, problem_statement, proposed_solution, target_user MUST be written in %s.
- example_quotes: up to 5 short VERBATIM quotes copied from the evidence, in their ORIGINAL language — never translate or fabricate quotes.
- domain_tags: 1-5 canonical ENGLISH slugs (lowercase, dash-separated). Never translate tags.
- urgency_score: integer 1-5 (how acute/frequent the pain looks in the evidence).
- monetization_signal: integer 0-5, 0 = no signal at all (any mention of paying/pricing raises it).
- known_competitors_ai_guess: short informational note, phrased as an unverified AI guess; empty string if you have none.`

// coherenceSystem: sentezden önce kova içi tutarlılık denetimi. Tema
// anahtarı tek tag olduğundan aynı kovada FARKLI dertler birikebilir;
// eşik "tag frekansı" değil "aynı derdin frekansı" olsun diye LLM en
// büyük tutarlı alt kümeyi seçer.
const coherenceSystem = `You are a strict evidence auditor. You receive numbered posts that were grouped under one broad topic tag. Identify the LARGEST subset of posts that describe the SAME SPECIFIC user pain or need — not merely the same broad topic. Posts about different pains within the same topic do NOT belong in the same subset.

Return ONLY a JSON object exactly of this shape: {"indices":[0,1,3]} — "indices" is an ARRAY of INTEGER post numbers (0-based), one integer per selected post. If every post describes a different pain, return the single most promising post's index. Never invent indices that were not given.`

type coherenceResponse struct {
	Indices []json.RawMessage `json:"indices"`
}

// coherenceIndices, tutarlılık cevabındaki indeksleri savunmacı ayrıştırır:
// tam sayı, sayı-string ("3"), bitişik rakamlar ("013" -> 0,1,3) ve virgüllü
// string ("0,1,3") kabul edilir — küçük modeller format talimatına her zaman
// uymuyor. n = kanıt sayısı; aralık dışı ve tekrar eden indeksler elenir.
func coherenceIndices(raw string, n int) ([]int, error) {
	var resp coherenceResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("tutarlılık cevabı parse: %w", err)
	}

	seen := map[int]bool{}
	var out []int
	add := func(v int) {
		if v >= 0 && v < n && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	for _, rm := range resp.Indices {
		var iv int
		if json.Unmarshal(rm, &iv) == nil {
			if iv < n {
				add(iv)
			} else {
				// Aralık dışı çok haneli sayı = bitiştirilmiş tek haneli
				// indeksler (örn. 13567 -> 1,3,5,6,7); kanıt <=8 olduğundan
				// geçerli indeks tek hanelidir.
				for _, r := range strconv.Itoa(iv) {
					add(int(r - '0'))
				}
			}
			continue
		}
		var sv string
		if json.Unmarshal(rm, &sv) != nil {
			continue
		}
		// String içindeki rakam gruplarını çıkar ("0,1,3", " 2 ", "013"...)
		for _, run := range digitRuns(sv) {
			v, err := strconv.Atoi(run)
			if err != nil {
				continue
			}
			if v < n {
				add(v)
				continue
			}
			// Aralık dışı çok haneli sayı = bitişik tek haneli indeksler
			// (kanıt <=8 olduğundan geçerli indeks tek hanelidir).
			for _, r := range run {
				add(int(r - '0'))
			}
		}
	}
	return out, nil
}

// digitRuns, string'deki ardışık rakam gruplarını döner.
func digitRuns(s string) []string {
	var runs []string
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			runs = append(runs, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		runs = append(runs, s[start:])
	}
	return runs
}

// coherentSubset, kanıt listesinden aynı spesifik derdi anlatan en büyük
// alt kümeyi seçer.
func coherentSubset(ctx context.Context, chat llm.Chat, evidence []store.RawPost) ([]store.RawPost, error) {
	var sb strings.Builder
	sb.WriteString("Posts:\n\n")
	for i, p := range evidence {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i, clip(p.Title, 200), clip(p.Body, 500))
	}

	raw, err := chat.ChatJSON(ctx, coherenceSystem, sb.String())
	if err != nil {
		return nil, err
	}
	indices, err := coherenceIndices(raw, len(evidence))
	if err != nil {
		return nil, err
	}

	var subset []store.RawPost
	for _, idx := range indices {
		subset = append(subset, evidence[idx])
	}
	return subset, nil
}

// Dedup eşikleri (#14): >= dupAutoThreshold kesin mükerrer; grey bölge
// [dupGreyThreshold, dupAutoThreshold) LLM hakemine sorulur.
const dupAutoThreshold = 0.80
const dupGreyThreshold = 0.45

const dupJudgeSystem = `You compare two software product idea cards and decide if they describe the SAME product idea (same underlying user need and essentially the same solution) — not merely the same broad domain. Return ONLY a JSON object: {"same": true} or {"same": false}.`

// findDuplicate, yeni fikrin mevcut kartların mükerreri olup olmadığına
// karar verir: pg_trgm benzerliği yüksekse doğrudan, gri bölgedeyse LLM
// hakemiyle.
func findDuplicate(ctx context.Context, st *store.Store, chat llm.Chat, idea store.Idea) (bool, store.Idea, error) {
	existing, sim, err := st.FindSimilarIdea(ctx, idea.Title, idea.ProblemStatement)
	if err != nil || existing.ID == 0 {
		return false, store.Idea{}, err
	}
	if sim >= dupAutoThreshold {
		return true, existing, nil
	}
	if sim < dupGreyThreshold {
		return false, store.Idea{}, nil
	}

	user := fmt.Sprintf("Idea A:\nTitle: %s\nProblem: %s\n\nIdea B:\nTitle: %s\nProblem: %s",
		existing.Title, clip(existing.ProblemStatement, 600),
		idea.Title, clip(idea.ProblemStatement, 600))
	raw, err := chat.ChatJSON(ctx, dupJudgeSystem, user)
	if err != nil {
		// Hakem ulaşılamazsa temkinli davran: mükerrer sayma, kartı yaz.
		log.Printf("synthesize: dedup hakemi HATA: %v — kart yazılıyor", err)
		return false, store.Idea{}, nil
	}
	var verdict struct {
		Same bool `json:"same"`
	}
	if err := json.Unmarshal([]byte(raw), &verdict); err != nil {
		return false, store.Idea{}, nil
	}
	if verdict.Same {
		return true, existing, nil
	}
	return false, store.Idea{}, nil
}

// SynthesizeIdeas, frekans eşiğini geçen temalardan idea card üretir
// (source_type=pain_point). Tema bazlı tekrar üretimi ThemesReadyForSynthesis
// engeller; fikir bazlı mükerrerlik findDuplicate ile yakalanır (#14).
func SynthesizeIdeas(ctx context.Context, cfg *config.Config, st *store.Store, chat llm.Chat) (int, error) {
	themes, err := st.ThemesReadyForSynthesis(ctx, cfg.MinThemeEvidence, synthesizeThemeLimit)
	if err != nil {
		return 0, err
	}
	if len(themes) == 0 {
		log.Printf("synthesize: eşiği (%d) geçen yeni tema yok", cfg.MinThemeEvidence)
		return 0, nil
	}

	created := 0
	for i, th := range themes {
		if ctx.Err() != nil {
			return created, ctx.Err()
		}

		evidence, err := st.ThemeEvidence(ctx, th.ID, evidencePerTheme)
		if err != nil {
			return created, err
		}
		if len(evidence) == 0 {
			continue
		}

		// Kova içi tutarlılık: eşik, tag frekansını değil aynı derdin
		// frekansını ölçsün.
		subset, err := coherentSubset(ctx, chat, evidence)
		if err != nil {
			log.Printf("synthesize: tema %q tutarlılık HATA: %v — atlandı", th.Name, err)
			continue
		}
		if len(subset) == 0 {
			// Prompt en az 1 indeks garanti eder; boş küme = bozuk model
			// cevabı. Temayı damgalamadan atla — sonraki koşuda yeniden
			// denenir.
			log.Printf("synthesize: tema %q tutarlılık cevabı boş — atlandı (işaretlenmedi)", th.Name)
			continue
		}
		if len(subset) < cfg.MinThemeEvidence {
			if err := st.MarkThemeIncoherent(ctx, th.ID); err != nil {
				return created, err
			}
			log.Printf("synthesize: tema %q tutarsız (%d/%d aynı dert) — yeni kanıta kadar beklemede",
				th.Name, len(subset), len(evidence))
			continue
		}

		idea, err := synthesizeOne(ctx, cfg, chat, th, subset)
		if errors.Is(err, errVendorInternal) {
			// Belirli bir ürünün kendi kusuru — kart olamaz. Yeni kanıt
			// gelene dek tema yeniden değerlendirilmez.
			if err := st.MarkThemeIncoherent(ctx, th.ID); err != nil {
				return created, err
			}
			log.Printf("synthesize: tema %q vendor-internal — kart üretilmedi", th.Name)
			continue
		}
		if err != nil {
			log.Printf("synthesize: tema %q HATA: %v — atlandı", th.Name, err)
			continue
		}

		// Dedup (#14): pg_trgm benzerliği + gri bölgede LLM hakemi. Mükerrer
		// fikir yeni kart açmaz, mevcut kartın kanıtını güçlendirir.
		dup, existing, err := findDuplicate(ctx, st, chat, idea)
		if err != nil {
			return created, err
		}
		if dup {
			if err := st.MergeIdeaEvidence(ctx, existing.ID, idea.EvidenceCount); err != nil {
				return created, err
			}
			if err := st.MarkThemeMerged(ctx, th.ID, existing.ID); err != nil {
				return created, err
			}
			log.Printf("synthesize: %q mükerrer -> %q kartına katıldı (kanıt +%d)",
				idea.Title, existing.Title, idea.EvidenceCount)
			continue
		}

		if _, err := st.InsertIdea(ctx, idea); err != nil {
			return created, err
		}
		created++
		log.Printf("synthesize: idea üretildi: %q (tema=%s, kanıt=%d)", idea.Title, th.Name, idea.EvidenceCount)

		if i < len(themes)-1 {
			select {
			case <-time.After(time.Duration(cfg.LLMSleepMS) * time.Millisecond):
			case <-ctx.Done():
				return created, ctx.Err()
			}
		}
	}
	return created, nil
}

func synthesizeOne(ctx context.Context, cfg *config.Config, chat llm.Chat, th store.Theme, evidence []store.RawPost) (store.Idea, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Theme: %s (seen in %d posts)\n\nEvidence posts:\n\n", th.Name, th.Frequency)
	for _, p := range evidence {
		fmt.Fprintf(&sb, "--- [%s/%s] %s\n%s\n\n", p.Platform, p.Community, clip(p.Title, 200), clip(p.Body, 900))
	}

	system := fmt.Sprintf(synthesizeSystemTmpl, langName(cfg.OutputLang))
	raw, err := chat.ChatJSON(ctx, system, sb.String())
	if err != nil {
		return store.Idea{}, err
	}

	idea, err := parseIdeaResponse(raw)
	if err != nil {
		return store.Idea{}, err
	}
	idea.SourceType = "pain_point"
	idea.SourceThemeID = &th.ID
	// Kanıt sayısı = tutarlılık denetiminden geçen post sayısı (tema
	// frekansı değil — kartın iddiası kadar kanıtı olsun).
	idea.EvidenceCount = len(evidence)
	return idea, nil
}

type ideaResponse struct {
	Title                   string   `json:"title"`
	ProblemStatement        string   `json:"problem_statement"`
	ProposedSolution        string   `json:"proposed_solution"`
	TargetUser              string   `json:"target_user"`
	ExampleQuotes           []string `json:"example_quotes"`
	UrgencyScore            int      `json:"urgency_score"`
	MonetizationSignal      int      `json:"monetization_signal"`
	KnownCompetitorsAIGuess string   `json:"known_competitors_ai_guess"`
	DomainTags              []string `json:"domain_tags"`
}

// parseIdeaResponse, LLM çıktısını doğrular: skorlar aralığa kıstırılır
// (urgency 1-5, monetization 0-5), tag'ler slug'a normalize edilir,
// alıntılar 5 ile sınırlanır.
// errVendorInternal: kanıt yalnızca belirli bir ürünün kendi kusurlarını
// anlatıyor — üçüncü tarafça inşa edilebilir bir fikir yok (skip cevabı).
var errVendorInternal = fmt.Errorf("vendor-internal: üçüncü tarafça inşa edilemez")

func parseIdeaResponse(raw string) (store.Idea, error) {
	var skip struct {
		Skip bool `json:"skip"`
	}
	if json.Unmarshal([]byte(raw), &skip) == nil && skip.Skip {
		return store.Idea{}, errVendorInternal
	}

	var r ideaResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return store.Idea{}, fmt.Errorf("LLM yanıtı JSON değil: %w (yanıt: %.200s)", err, raw)
	}
	if strings.TrimSpace(r.Title) == "" {
		return store.Idea{}, fmt.Errorf("LLM yanıtında title yok (yanıt: %.200s)", raw)
	}

	quotes := r.ExampleQuotes
	if len(quotes) > 5 {
		quotes = quotes[:5]
	}
	if quotes == nil {
		quotes = []string{}
	}

	return store.Idea{
		Title:                   strings.TrimSpace(r.Title),
		ProblemStatement:        strings.TrimSpace(r.ProblemStatement),
		ProposedSolution:        strings.TrimSpace(r.ProposedSolution),
		TargetUser:              strings.TrimSpace(r.TargetUser),
		ExampleQuotes:           quotes,
		UrgencyScore:            clamp(r.UrgencyScore, 1, 5),
		MonetizationSignal:      clamp(r.MonetizationSignal, 0, 5),
		KnownCompetitorsAIGuess: strings.TrimSpace(r.KnownCompetitorsAIGuess),
		DomainTags:              normalizeTags(r.DomainTags),
	}, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
