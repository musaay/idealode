package pipeline

import (
	"context"
	"log"

	"github.com/musaay/idealode/api/internal/store"
)

// GroupThemes, sinyal taşıyan (pain_point / feature_request) analizleri tag
// bazlı temalara bağlar (V1: embeddingsiz — Groq embedding sunmuyor).
//
// Gruplama anahtarı post'un birincil domain_tag'i (classification prompt'u
// tag'leri en spesifikten sıralar). noise/complaint tema oluşturmaz.
func GroupThemes(ctx context.Context, st *store.Store) (int, error) {
	analyses, err := st.UnthemedAnalyses(ctx, 1000)
	if err != nil {
		return 0, err
	}
	if len(analyses) == 0 {
		return 0, nil
	}

	linked := 0
	for _, a := range analyses {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}
		if len(a.DomainTags) == 0 {
			continue
		}
		primary := a.DomainTags[0]

		themeID, err := st.UpsertTheme(ctx, primary)
		if err != nil {
			return linked, err
		}
		if err := st.LinkThemePost(ctx, themeID, a.PostID); err != nil {
			return linked, err
		}
		linked++
	}

	// frequency = theme_posts sayısı; tek yerde, tutarlı şekilde tazelenir.
	if err := st.RefreshThemeStats(ctx); err != nil {
		return linked, err
	}
	log.Printf("themes: %d post temalara bağlandı", linked)
	return linked, nil
}
