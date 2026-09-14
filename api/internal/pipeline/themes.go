package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// Kova başına en fazla bu kadar yeni gönderi LLM kümelemesine girer; prompt
// şişmesin diye (#127). Aşan kısım bu koşuda temasız bırakılır —
// UnthemedAnalyses zaten theme_posts'a bakar, kalan post'lar bir sonraki
// koşuda yeniden seçilir.
const themeClusterBucketLimit = 40

// themeClusterSystem: kova içi LLM kümeleme sistem prompt'u (#127). Kaba
// kova anahtarı (domain_tag) tek bir geniş kategori olduğundan aynı kovada
// FARKLI spesifik dertler birikir ("machine-learning" altında onlarca
// alakasız şikâyet gibi); model her gönderiyi mevcut bir temaya (aynı
// spesifik dert) atar ya da yeni, spesifik bir tema adı üretir — kategori
// adı DEĞİL.
const themeClusterSystem = `You cluster social/developer-platform posts, all currently grouped under one broad topic tag, into SPECIFIC underlying pain points ("themes"). The topic tag itself (e.g. "machine-learning", "mobile-banking") is too broad — many DIFFERENT specific complaints share it. Your job: assign each post to a theme that names ONE SPECIFIC pain or need, never the broad category.

You receive:
1. EXISTING theme names already used in this topic (may be empty).
2. NEW numbered posts (title + short body) not yet assigned to any theme.

For each post, either:
- reuse an EXISTING theme name EXACTLY as given, if the post describes the same specific pain, or
- invent a NEW theme name if none of the existing ones fit.

Theme name rules: a SPECIFIC pain/need, not a category; in ENGLISH; short (under 8 words); no product/brand names. Good: "cannot export chat history", "no bulk invoice download". Bad: "machine learning", "billing issues".

Return ONLY a JSON object: {"assignments":[{"post":0,"theme":"cannot export chat history"},{"post":1,"theme":"no bulk invoice download"}]}. Include one entry per post you can confidently classify; omit a post entirely if you are unsure which theme it belongs to.`

// themeAssignment, tema kümeleme cevabındaki tek bir atama (post indeksi ->
// tema adı). Post, esnek ayrıştırma için ham JSON tutulur (int ya da
// sayı-string gelebilir — parseFlexibleInt bunu çözer).
type themeAssignment struct {
	Post  json.RawMessage `json:"post"`
	Theme string          `json:"theme"`
}

type themeClusterResponse struct {
	Assignments []themeAssignment `json:"assignments"`
}

// GroupThemes, sinyal taşıyan (pain_point / feature_request) analizleri iki
// aşamada temaya bağlar (#127):
//
//  1. Kaba kova (mevcut davranış korunur): birincil domain_tag'e göre
//     gruplanır. noise/complaint ve etiketsiz gönderiler UnthemedAnalyses
//     tarafından zaten elenir.
//  2. Kova içi LLM kümeleme: kova başına TEK çağrı — model her gönderiyi
//     kovanın MEVCUT temalarından birine atar ya da yeni, spesifik bir tema
//     adı üretir (bkz. themeClusterSystem).
//
// LLM hata verirse (429/ağ), bozuk/boş JSON dönerse ya da bir gönderiyi hiç
// atamazsa, o gönderi ESKİ davranışa (domain_tag'i doğrudan tema adı sayma)
// düşer — hiçbir gönderi temasız kalmaz.
func GroupThemes(ctx context.Context, st *store.Store, chat llm.Chat) (int, error) {
	analyses, err := st.UnthemedAnalyses(ctx, 1000)
	if err != nil {
		return 0, err
	}
	if len(analyses) == 0 {
		return 0, nil
	}

	bucketOrder, buckets := groupByDomainTag(analyses)

	linked, clustered, fallback := 0, 0, 0
	for _, tag := range bucketOrder {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}
		bucket := buckets[tag]

		existing, err := st.ThemesByDomainTag(ctx, tag)
		if err != nil {
			return linked, fmt.Errorf("kova %q mevcut temaları: %w", tag, err)
		}

		assignments := clusterBucket(ctx, chat, tag, existing, bucket)

		for i, a := range bucket {
			themeName, ok := assignments[i]
			if !ok || themeName == "" {
				// Eski davranış: kova etiketi doğrudan tema adı.
				themeName = tag
				fallback++
			} else {
				clustered++
			}

			themeID, err := st.UpsertTheme(ctx, themeName, tag)
			if err != nil {
				return linked, err
			}
			if err := st.LinkThemePost(ctx, themeID, a.PostID); err != nil {
				return linked, err
			}
			linked++
		}
	}

	// frequency = theme_posts sayısı; tek yerde, tutarlı şekilde tazelenir.
	if err := st.RefreshThemeStats(ctx); err != nil {
		return linked, err
	}
	log.Printf("themes: %d post temalara bağlandı (%d LLM kümeleme, %d eski davranış)", linked, clustered, fallback)
	return linked, nil
}

// groupByDomainTag, kaba kova adımı: analizleri birincil domain_tag'e göre
// gruplar (ilk görülme sırası korunur — belirli/tekrarlanabilir davranış)
// ve her kovayı en fazla themeClusterBucketLimit post ile sınırlar (#127).
// Sınırı aşan post'lar döndürülen kovada YER ALMAZ; UnthemedAnalyses zaten
// yalnız temasız post'ları getirdiğinden bir sonraki koşuda yeniden seçilir.
func groupByDomainTag(analyses []store.PostAnalysis) (order []string, buckets map[string][]store.PostAnalysis) {
	buckets = map[string][]store.PostAnalysis{}
	for _, a := range analyses {
		if len(a.DomainTags) == 0 {
			continue
		}
		primary := a.DomainTags[0]
		if _, ok := buckets[primary]; !ok {
			order = append(order, primary)
		}
		buckets[primary] = append(buckets[primary], a)
	}
	for tag, bucket := range buckets {
		if len(bucket) > themeClusterBucketLimit {
			buckets[tag] = bucket[:themeClusterBucketLimit]
		}
	}
	return order, buckets
}

// clusterBucket, TEK bir domain_tag kovası için TEK LLM çağrısı yapar ve
// bucket içindeki her post için (0-bazlı indeks -> tema adı) haritası
// döner. LLM çağrısı hata verirse ya da cevap bozuk/boş JSON'sa nil döner
// (tek satır TR log burada basılır) — çağıran GroupThemes her postu ESKİ
// davranışa (domain_tag) düşürür. Model bir postu hiç atamazsa o postun
// indeksi haritada yer almaz; aynı geri düşüş yalnız o post için uygulanır.
func clusterBucket(ctx context.Context, chat llm.Chat, tag string, existing []store.Theme, bucket []store.PostAnalysis) map[int]string {
	user := themeClusterUserPrompt(existing, bucket)
	// Yargı çağrısı (tema kümeleme): sıcaklık 0 — tutarlı karar (#106).
	raw, err := chat.ChatJSONWithTemperature(ctx, themeClusterSystem, user, 0)
	if err != nil {
		log.Printf("temalar: kova %q için LLM HATA: %v — eski davranışa düşülüyor", tag, err)
		return nil
	}

	assignments, err := parseThemeAssignments(raw, len(bucket))
	if err != nil {
		log.Printf("temalar: kova %q LLM cevabı bozuk: %v — eski davranışa düşülüyor", tag, err)
		return nil
	}
	return assignments
}

// themeClusterUserPrompt, kova içi kümeleme kullanıcı prompt'unu kurar:
// mevcut tema adları (varsa) + numaralı yeni gönderiler (başlık + kısa
// gövde). Numaralandırma coherentSubset'teki desenle aynıdır (0-bazlı).
func themeClusterUserPrompt(existing []store.Theme, bucket []store.PostAnalysis) string {
	var sb strings.Builder
	sb.WriteString("Existing themes:\n")
	if len(existing) == 0 {
		sb.WriteString("(none yet)\n")
	} else {
		for _, t := range existing {
			fmt.Fprintf(&sb, "- %s\n", t.Name)
		}
	}
	sb.WriteString("\nNew posts:\n\n")
	for i, a := range bucket {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i, clip(a.Title, 200), clip(a.Body, 500))
	}
	return sb.String()
}

// parseThemeAssignments, tema kümeleme cevabını savunmacı ayrıştırır. Üst
// seviye JSON parse edilemezse (boş cevap dahil) hata döner — çağıran bunu
// "bozuk JSON" sayıp tüm kovayı eski davranışa düşürür. Geçerli JSON
// içindeki her atama ayrı ayrı doğrulanır: aralık dışı/ayrıştırılamayan
// post indeksi ve boş tema adı SESSİZCE elenir (o postun indeksi haritada
// yer almaz — çağıran bunu post bazlı geri düşüş sayar, hata değil). Aynı
// post indeksi birden fazla kez gelirse İLK geçerli atama kalır.
func parseThemeAssignments(raw string, n int) (map[int]string, error) {
	var resp themeClusterResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, fmt.Errorf("tema kümeleme cevabı parse: %w", err)
	}

	out := map[int]string{}
	for _, a := range resp.Assignments {
		idx, ok := parseFlexibleInt(a.Post)
		if !ok || idx < 0 || idx >= n {
			continue
		}
		name := strings.TrimSpace(a.Theme)
		if name == "" {
			continue
		}
		if _, exists := out[idx]; !exists {
			out[idx] = name
		}
	}
	return out, nil
}

// parseFlexibleInt, JSON alanını int ya da sayısal string olarak kabul eder
// (küçük modeller post indeksini bazen string döner) — coherenceIndices'teki
// esneklik prensibiyle aynı doğrultuda.
func parseFlexibleInt(raw json.RawMessage) (int, bool) {
	var iv int
	if json.Unmarshal(raw, &iv) == nil {
		return iv, true
	}
	var sv string
	if json.Unmarshal(raw, &sv) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(sv)); err == nil {
			return n, true
		}
	}
	return 0, false
}
