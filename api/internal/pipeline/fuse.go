package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// Kanıt füzyonu (#43): market_derived kartlar gelir kanıtı taşır ama yerel
// talep kanıtı yoktur. Fuse, pipeline'daki analiz edilmiş şikayetlerden bu
// kartlara gerçekten aynı ihtiyacı anlatan post'ları eşleştirir — çift
// kanıtlı kart: gelir kanıtı (emsal) + talep kanıtı (birebir alıntılar).

// Koşu başına en fazla bu kadar kart füzyonlanır; kart başına aday sayısı.
const fuseIdeaLimit = 5
const fuseCandidateLimit = 10
const fuseEvidenceMax = 5

const fuseJudgeSystem = `You match a software product idea with user complaints/requests. You receive one idea (title + problem) and numbered posts. Select ONLY the posts that genuinely express demand for THIS idea — the underlying need must match, not merely the same broad domain. Be strict: a generic complaint about the domain does not count.

Return ONLY a JSON object exactly of this shape: {"indices":[0,3]} — an ARRAY of INTEGER post numbers (0-based). If none match, return {"indices":[]}.`

// FuseEvidence, füzyon bekleyen market_derived kartlara yerel talep kanıtı
// eşleştirir; işlenen kart sayısını döner.
func FuseEvidence(ctx context.Context, cfg *config.Config, st *store.Store, chat llm.Chat) (int, error) {
	ideas, err := st.IdeasNeedingFusion(ctx, fuseIdeaLimit)
	if err != nil {
		return 0, err
	}
	if len(ideas) == 0 {
		return 0, nil
	}

	fused := 0
	for i, idea := range ideas {
		if ctx.Err() != nil {
			return fused, ctx.Err()
		}

		candidates, err := st.FusionCandidates(ctx, idea.DomainTags, idea.ProblemStatement, fuseCandidateLimit)
		if err != nil {
			return fused, err
		}

		evidence := []string{}
		if len(candidates) > 0 {
			matched, err := fuseJudge(ctx, chat, idea, candidates)
			if err != nil {
				log.Printf("fuse: %q hakem HATA: %v — atlandı (damgalanmadı)", idea.Title, err)
				continue
			}
			for _, p := range matched {
				if len(evidence) >= fuseEvidenceMax {
					break
				}
				quote := strings.TrimSpace(p.Body)
				if quote == "" {
					quote = p.Title
				}
				evidence = append(evidence,
					fmt.Sprintf("%s — [%s] %s", clip(quote, 260), p.Platform, p.URL))
			}
		}

		if err := st.SetIdeaLocalEvidence(ctx, idea.ID, evidence); err != nil {
			return fused, err
		}
		fused++
		log.Printf("fuse: %q -> %d yerel talep kanıtı", idea.Title, len(evidence))

		if i < len(ideas)-1 {
			select {
			case <-time.After(time.Duration(cfg.LLMSleepMS) * time.Millisecond):
			case <-ctx.Done():
				return fused, ctx.Err()
			}
		}
	}
	return fused, nil
}

// fuseJudge, adaylardan fikirle gerçekten eşleşenleri LLM'e seçtirir.
func fuseJudge(ctx context.Context, chat llm.Chat, idea store.Idea, candidates []store.RawPost) ([]store.RawPost, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Idea:\nTitle: %s\nProblem: %s\n\nPosts:\n\n",
		idea.Title, clip(idea.ProblemStatement, 500))
	for i, p := range candidates {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i, clip(p.Title, 150), clip(p.Body, 350))
	}

	raw, err := chat.ChatJSON(ctx, fuseJudgeSystem, sb.String())
	if err != nil {
		return nil, err
	}
	indices, err := coherenceIndices(raw, len(candidates))
	if err != nil {
		return nil, err
	}

	var out []store.RawPost
	for _, idx := range indices {
		out = append(out, candidates[idx])
	}
	return out, nil
}
