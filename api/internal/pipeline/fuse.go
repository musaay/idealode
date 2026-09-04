package pipeline

import (
	"context"
	"fmt"
	"log"
	"regexp"
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

// İvme geçişi (#50 B parçası): github_trending eşleşmeleri talep
// alıntılarının 5'lik tavanını yemesin diye AYRI ve daha düşük bir tavan
// taşır.
const fuseMomentumCandidateLimit = 10
const fuseMomentumMax = 2

const fuseJudgeSystem = `You match a software product idea with user complaints/requests. You receive one idea (title + problem) and numbered posts. Select ONLY the posts that genuinely express demand for THIS idea — the underlying need must match, not merely the same broad domain. Be strict: a generic complaint about the domain does not count.

Return ONLY a JSON object exactly of this shape: {"indices":[0,3]} — an ARRAY of INTEGER post numbers (0-based). If none match, return {"indices":[]}.`

// fuseMomentumSystem: ivme geçişinin hakemi — talep değil, "bu fikre gerçekten
// hizmet eden ya da doğrudan emsal olan" trend repo seçimi.
const fuseMomentumSystem = `You match a software product idea with trending GitHub repositories (numbered, each shown as "owner/repo" plus a short description). Select ONLY repos that genuinely serve THIS idea's need or are a direct precedent — not merely the same broad domain.

Return ONLY a JSON object exactly of this shape: {"indices":[0,3]} — an ARRAY of INTEGER repo numbers (0-based). If none match, return {"indices":[]}.`

// fuseStore, FuseEvidence'ın ihtiyaç duyduğu store operasyonlarını soyutlar
// — canlı DB olmadan fake ile test edilebilsin diye (bkz. fuse_test.go).
// *store.Store bu arayüzü zaten sağlar.
type fuseStore interface {
	IdeasNeedingFusion(ctx context.Context, limit int) ([]store.Idea, error)
	FusionCandidates(ctx context.Context, tags []string, problem string, limit int) ([]store.RawPost, error)
	SetIdeaLocalEvidence(ctx context.Context, ideaID int64, evidence []string) error
	MomentumCandidates(ctx context.Context, problem string, tags []string, limit int) ([]store.RawPost, error)
	AppendIdeaLocalEvidence(ctx context.Context, ideaID int64, lines []string) error
}

// FuseEvidence, füzyon bekleyen market_derived kartlara yerel talep kanıtı
// eşleştirir; işlenen kart sayısını döner.
func FuseEvidence(ctx context.Context, cfg *config.Config, st fuseStore, chat llm.Chat) (int, error) {
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

		// fused_at daha önce damgalanmışsa (haftalık yeniden deneme) talep
		// hakemi TEKRAR çağrılmaz — LLM maliyeti bir kez ödenir; yalnız
		// aşağıdaki ivme geçişi çalışır.
		isRetry := idea.FusedAt != nil

		if !isRetry {
			// İlk geçiş: talep + ivme AYNI koşuda hesaplanır ve TEK
			// SetIdeaLocalEvidence çağrısıyla yazılır (fused_at bir kez
			// damgalanır) — Append yalnız haftalık yeniden deneme yolunda
			// kullanılır (aşağıdaki else dalı).
			candidates, err := st.FusionCandidates(ctx, idea.DomainTags, idea.ProblemStatement, fuseCandidateLimit)
			if err != nil {
				return fused, err
			}

			demand := []string{}
			if len(candidates) > 0 {
				matched, err := fuseJudge(ctx, chat, idea, candidates)
				if err != nil {
					log.Printf("fuse: %q talep hakemi HATA: %v — atlandı (damgalanmadı)", idea.Title, err)
					continue
				}
				for _, p := range matched {
					if len(demand) >= fuseEvidenceMax {
						break
					}
					quote := strings.TrimSpace(p.Body)
					if quote == "" {
						quote = p.Title
					}
					demand = append(demand,
						fmt.Sprintf("%s — [%s] %s", clip(quote, 260), p.Platform, p.URL))
				}
			}

			// İvme hakemi hata verirse talep kanıtı YİNE de yazılır — kaybolmaz;
			// yalnız ivme boş kalır ve haftalık yeniden denemede tekrar denenir.
			momentum, err := fuseMomentum(ctx, chat, st, idea)
			if err != nil {
				log.Printf("fuse: %q ivme hakemi HATA: %v — talep kanıtı yazıldı, ivme haftalık yeniden denemede", idea.Title, err)
				momentum = nil
			}

			evidence := append(append([]string{}, demand...), momentum...)
			if err := st.SetIdeaLocalEvidence(ctx, idea.ID, evidence); err != nil {
				return fused, err
			}
			log.Printf("fuse: %q -> %d yerel talep + %d ivme kanıtı", idea.Title, len(demand), len(momentum))
		} else {
			// Haftalık yeniden deneme: yalnız ivme geçişi çalışır; mevcut
			// talep kanıtı satırları KORUNUR (Append, Set değil).
			momentum, err := fuseMomentum(ctx, chat, st, idea)
			if err != nil {
				log.Printf("fuse: %q ivme geçişi HATA: %v — atlandı (damgalanmadı)", idea.Title, err)
				continue
			}
			if err := st.AppendIdeaLocalEvidence(ctx, idea.ID, momentum); err != nil {
				return fused, err
			}
			log.Printf("fuse: %q -> %d ivme kanıtı (yeniden deneme)", idea.Title, len(momentum))
		}

		fused++

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

// totalStarsPrefixRe, github_trending connector'ının Body alanına gömdüğü
// "★N · açıklama" önekindeki toplam yıldız sayısını yakalar.
var totalStarsPrefixRe = regexp.MustCompile(`^★([\d,]+)`)

// fuseMomentum, karta aday GitHub ivme repolarını bulur, hakeme seçtirir ve
// view.go:293 regex'ine uyan kanıt satırlarına çevirir (en fazla
// fuseMomentumMax adet — talep alıntılarının tavanından AYRI). Aday yoksa
// LLM'e hiç gidilmez, boş (nil değil) dizi döner.
func fuseMomentum(ctx context.Context, chat llm.Chat, st fuseStore, idea store.Idea) ([]string, error) {
	candidates, err := st.MomentumCandidates(ctx, idea.ProblemStatement, idea.DomainTags, fuseMomentumCandidateLimit)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []string{}, nil
	}

	matched, err := fuseMomentumJudge(ctx, chat, idea, candidates)
	if err != nil {
		return nil, err
	}

	lines := []string{}
	for _, p := range matched {
		if len(lines) >= fuseMomentumMax {
			break
		}
		stars := "?"
		if m := totalStarsPrefixRe.FindStringSubmatch(p.Body); m != nil {
			stars = m[1]
		}
		lines = append(lines,
			fmt.Sprintf("%s ★%s, +%d/gün — [%s] %s", p.Title, stars, p.Score, p.Platform, p.URL))
	}
	return lines, nil
}

// fuseMomentumJudge, aday trend repolardan fikirle gerçekten ilgili olanları
// LLM'e seçtirir (talep hakeminden AYRI hakem — farklı system prompt).
func fuseMomentumJudge(ctx context.Context, chat llm.Chat, idea store.Idea, candidates []store.RawPost) ([]store.RawPost, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Idea:\nTitle: %s\nProblem: %s\n\nRepos:\n\n",
		idea.Title, clip(idea.ProblemStatement, 500))
	for i, p := range candidates {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i, p.Title, clip(p.Body, 350))
	}

	raw, err := chat.ChatJSON(ctx, fuseMomentumSystem, sb.String())
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
