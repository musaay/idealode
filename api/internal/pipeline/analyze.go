package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/prefilter"
	"github.com/musaay/idealode/api/internal/store"
)

// Koşu başına en fazla bu kadar analiz edilmemiş post işlenir.
const analyzeBatchLimit = 500

var validClassifications = map[string]bool{
	"pain_point":      true,
	"feature_request": true,
	"complaint":       true,
	"noise":           true,
}

// Analyze, analiz edilmemiş post'ları ön-filtreden geçirir, kalanları Groq
// ile chunk'lar hâlinde sınıflandırır ve post_analysis'e yazar.
func Analyze(ctx context.Context, cfg *config.Config, st *store.Store, chat llm.Chat) (int, error) {
	posts, err := st.UnanalyzedPosts(ctx, analyzeBatchLimit)
	if err != nil {
		return 0, err
	}
	if len(posts) == 0 {
		log.Printf("analyze: analiz edilmemiş post yok")
		return 0, nil
	}

	// 1) Ön-filtre — sinyalsiz post'lar LLM'e hiç gitmez (maliyet).
	var toLLM []store.RawPost
	var filtered []store.PostAnalysis
	for _, p := range posts {
		if prefilter.ShouldAnalyze(p.Platform, p.Title, p.Body) {
			toLLM = append(toLLM, p)
		} else {
			filtered = append(filtered, store.PostAnalysis{
				PostID:         p.ID,
				Classification: "noise",
				Prefiltered:    true,
			})
		}
	}
	if err := st.InsertPostAnalyses(ctx, filtered); err != nil {
		return 0, err
	}
	log.Printf("analyze: %d post -> ön-filtre: %d elendi, %d LLM'e gidiyor",
		len(posts), len(filtered), len(toLLM))

	// 2) Classification — chunk'lar hâlinde, chunk'lar arası sabit sleep
	// ("sakin ilerleme"; sleep + retry birlikte).
	processed := len(filtered)
	for i := 0; i < len(toLLM); i += cfg.LLMChunkSize {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		chunk := toLLM[i:min(i+cfg.LLMChunkSize, len(toLLM))]

		analyses, err := classifyChunk(ctx, cfg, chat, chunk)
		if err != nil {
			// Bozuk chunk pipeline'ı düşürmez: logla, atla, devam et.
			log.Printf("analyze: chunk %d-%d HATA: %v — atlandı", i, i+len(chunk), err)
		} else {
			if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
				return processed, err
			}
			processed += len(analyses)
		}

		if i+cfg.LLMChunkSize < len(toLLM) {
			select {
			case <-time.After(time.Duration(cfg.LLMSleepMS) * time.Millisecond):
			case <-ctx.Done():
				return processed, ctx.Err()
			}
		}
	}

	log.Printf("analyze: toplam %d post_analysis yazıldı", processed)
	return processed, nil
}

const classifySystemTmpl = `You are a classifier for a software-idea mining pipeline. You receive social/developer platform posts. For EACH post, decide whether it expresses a real software-related need.

Return ONLY a JSON object of the form:
{"results":[{"id":<post id>,"classification":"pain_point|feature_request|complaint|noise","problem_summary":"...","target_audience":"...","domain_tags":["slug-one","slug-two"],"willingness_to_pay":true|false}]}

Rules:
- classification: "pain_point" = a real recurring problem someone wants solved; "feature_request" = a concrete ask for a capability/tool; "complaint" = venting without an actionable need; "noise" = none of these.
- problem_summary and target_audience MUST be written in %s. For noise/complaint they may be empty strings.
- domain_tags: 1-5 canonical ENGLISH slugs (lowercase, dash-separated, e.g. "invoice-automation", "browser-extension"). Never translate tags.
- willingness_to_pay: true only if the author signals they would pay / already pay / mention pricing.
- Include EVERY input post id exactly once. Output JSON only.`

func classifyChunk(ctx context.Context, cfg *config.Config, chat llm.Chat, chunk []store.RawPost) ([]store.PostAnalysis, error) {
	var sb strings.Builder
	for _, p := range chunk {
		fmt.Fprintf(&sb, "### post id=%d platform=%s community=%q\ntitle: %s\nbody: %s\n\n",
			p.ID, p.Platform, p.Community, clip(p.Title, 300), clip(p.Body, 1500))
	}

	system := fmt.Sprintf(classifySystemTmpl, langName(cfg.OutputLang))
	raw, err := chat.ChatJSON(ctx, system, sb.String())
	if err != nil {
		return nil, err
	}
	return parseClassifyResponse(raw, chunk)
}

type classifyResponse struct {
	Results []struct {
		ID               int64    `json:"id"`
		Classification   string   `json:"classification"`
		ProblemSummary   string   `json:"problem_summary"`
		TargetAudience   string   `json:"target_audience"`
		DomainTags       []string `json:"domain_tags"`
		WillingnessToPay bool     `json:"willingness_to_pay"`
	} `json:"results"`
}

// parseClassifyResponse, LLM çıktısını doğrular: yalnız chunk'taki post
// id'leri kabul edilir, geçersiz classification değerleri noise'a düşer,
// tag'ler kanonik slug'a normalize edilir.
func parseClassifyResponse(raw string, chunk []store.RawPost) ([]store.PostAnalysis, error) {
	var resp classifyResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("LLM yanıtı JSON değil: %w (yanıt: %.200s)", err, raw)
	}

	valid := map[int64]bool{}
	for _, p := range chunk {
		valid[p.ID] = true
	}

	var out []store.PostAnalysis
	seen := map[int64]bool{}
	for _, r := range resp.Results {
		if !valid[r.ID] || seen[r.ID] {
			continue
		}
		seen[r.ID] = true

		cls := strings.ToLower(strings.TrimSpace(r.Classification))
		if !validClassifications[cls] {
			cls = "noise"
		}
		out = append(out, store.PostAnalysis{
			PostID:           r.ID,
			Classification:   cls,
			ProblemSummary:   strings.TrimSpace(r.ProblemSummary),
			TargetAudience:   strings.TrimSpace(r.TargetAudience),
			DomainTags:       normalizeTags(r.DomainTags),
			WillingnessToPay: r.WillingnessToPay,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("LLM yanıtında geçerli sonuç yok (yanıt: %.200s)", raw)
	}
	return out, nil
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeTags, tag'leri kanonik EN slug biçimine indirger ve en fazla 5
// benzersiz tag döner.
func normalizeTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		s := strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(t), "-"), "-")
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == 5 {
			break
		}
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// langName, OUTPUT_LANG kodunu prompt'ta kullanılacak dil adına çevirir.
func langName(code string) string {
	switch strings.ToLower(code) {
	case "tr":
		return "Turkish (Türkçe)"
	case "en":
		return "English"
	default:
		return code
	}
}
