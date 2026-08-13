package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

Return ONLY a JSON object:
{"title":"...","problem_statement":"...","proposed_solution":"...","target_user":"...","example_quotes":["..."],"urgency_score":1-5,"monetization_signal":0-5,"known_competitors_ai_guess":"...","domain_tags":["slug"]}

Rules:
- title, problem_statement, proposed_solution, target_user MUST be written in %s.
- example_quotes: up to 5 short VERBATIM quotes copied from the evidence, in their ORIGINAL language — never translate or fabricate quotes.
- domain_tags: 1-5 canonical ENGLISH slugs (lowercase, dash-separated). Never translate tags.
- urgency_score: integer 1-5 (how acute/frequent the pain looks in the evidence).
- monetization_signal: integer 0-5, 0 = no signal at all (any mention of paying/pricing raises it).
- known_competitors_ai_guess: short informational note, phrased as an unverified AI guess; empty string if you have none.`

// SynthesizeIdeas, frekans eşiğini geçen temalardan idea card üretir
// (source_type=pain_point). Aynı temadan ikinci kez üretilmez (Faz 0 basit
// dedup; pg_trgm dedup Faz 1 / issue #14).
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

		idea, err := synthesizeOne(ctx, cfg, chat, th, evidence)
		if err != nil {
			log.Printf("synthesize: tema %q HATA: %v — atlandı", th.Name, err)
			continue
		}

		// Naif başlık dedup'u (Faz 0): birebir aynı başlık varsa yazma.
		exists, err := st.IdeaTitleExists(ctx, idea.Title)
		if err != nil {
			return created, err
		}
		if exists {
			log.Printf("synthesize: %q zaten var (başlık eşleşmesi) — atlandı", idea.Title)
			continue
		}

		if _, err := st.InsertIdea(ctx, idea); err != nil {
			return created, err
		}
		created++
		log.Printf("synthesize: idea üretildi: %q (tema=%s, kanıt=%d)", idea.Title, th.Name, th.Frequency)

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
	idea.EvidenceCount = th.Frequency
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
func parseIdeaResponse(raw string) (store.Idea, error) {
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
