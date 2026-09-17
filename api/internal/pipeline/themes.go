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
// koşuda yeniden seçilir. Aynı sayı, partileme adımında parti başına üst
// sınır olarak da kullanılır (#149): bir partideki TOPLAM post sayısı da bu
// sınırı aşmaz.
const themeClusterBucketLimit = 40

// themeClusterSystem: parti içi LLM kümeleme sistem prompt'u (#127, #149).
// Kaba kova anahtarı (domain_tag) tek bir geniş kategori olduğundan aynı
// kovada FARKLI spesifik dertler birikir ("machine-learning" altında
// onlarca alakasız şikâyet gibi); model her gönderiyi mevcut bir temaya
// (aynı spesifik dert) atar ya da yeni, spesifik bir tema adı üretir —
// kategori adı DEĞİL. Küçük kovalar tek çağrıda BİRDEN FAZLA domain_tag ile
// birlikte gelebilir (#149 — çağrı sızıntısını önlemek için partileme);
// prompt bunu açıkça belirtir ve etiketler arası atamayı yasaklar.
const themeClusterSystem = `You cluster social/developer-platform posts into SPECIFIC underlying pain points ("themes"). Posts are grouped by a broad topic tag (e.g. "machine-learning", "mobile-banking") that is too broad — many DIFFERENT specific complaints share the same tag. Your job: assign each post to a theme that names ONE SPECIFIC pain or need, never the broad category.

For efficiency, this request may bundle posts from SEVERAL different tags together. Each post is still labeled with its own tag, and each tag's existing themes are listed separately.

You receive:
1. EXISTING theme names, grouped by tag (a tag's list may be empty).
2. NEW numbered posts (tag + title + short body) not yet assigned to any theme.

For each post, either:
- reuse an EXISTING theme name EXACTLY as given, ONLY from the list under that post's OWN tag, if the post describes the same specific pain, or
- invent a NEW theme name if none of its own tag's existing themes fit.

STRICT RULE: never assign a post to a theme listed under a DIFFERENT tag's section, even if it looks related. Each tag's existing themes are isolated from every other tag — cross-tag assignment is not allowed and will be rejected.

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

// postBatch, packBuckets'ın ürettiği TEK LLM çağrısı birimidir: bir ya da
// daha fazla domain_tag kovasının ardışık birleşimi (#149). posts ve
// postTags aynı uzunlukta ve aynı sırada eşlenir (postTags[i], posts[i]'nin
// ait olduğu domain_tag'idir) — LLM cevabındaki 0-bazlı indeks bu sıraya
// göre çözülür. tags, bu partide yer alan domain_tag'lerin sırasıdır
// (prompt'ta "mevcut temalar" bölümü bu sırayla yazılır).
type postBatch struct {
	tags     []string
	posts    []store.PostAnalysis
	postTags []string
}

// GroupThemes, sinyal taşıyan (pain_point / feature_request) analizleri iki
// aşamada temaya bağlar (#127, #149):
//
//  1. Kaba kova (mevcut davranış korunur): birincil domain_tag'e göre
//     gruplanır. noise/complaint ve etiketsiz gönderiler UnthemedAnalyses
//     tarafından zaten elenir.
//  2. Parti paketleme + kova içi LLM kümeleme: kovalar açgözlü şekilde
//     partilere paketlenir (parti toplamı themeClusterBucketLimit'i
//     aşmaz), parti başına TEK çağrı — model her gönderiyi KENDİ
//     domain_tag'inin mevcut temalarından birine atar ya da yeni, spesifik
//     bir tema adı üretir (bkz. themeClusterSystem). Küçük kovalar tek
//     çağrıda birleşir; bu, davranışı DEĞİŞTİRMEZ, yalnız çağrı sayısını
//     düşürür (#149 — 51 gönderi için 50 çağrı sorunu).
//
// LLM hata verirse (429/ağ), bozuk/boş JSON dönerse ya da bir gönderiyi hiç
// atamazsa, o gönderi ESKİ davranışa (domain_tag'i doğrudan tema adı sayma)
// düşer — hiçbir gönderi temasız kalmaz.
//
// Etiketler arası isim çakışması (#149 review bulgusu): theme_name DB'de
// GLOBAL UNIQUE'tir ve UpsertTheme çakışmada domain_tag'i GÜNCELLEMEZ.
// Model bir post'u partide GÖRÜNMEYEN başka bir etiketin mevcut temasıyla
// aynı adla (kaza ya da "yeni" sanıp) eşlerse, bellek-içi kontrol bunu asla
// göremez (yalnız aynı partideki etiketleri bilir). Bu yüzden asıl garanti
// DB SINIRINDA uygulanır: upsertThemeForPost, UpsertTheme'in döndürdüğü
// GERÇEK sahibi post'un kendi etiketiyle karşılaştırır; farklıysa o temaya
// BAĞLAMAZ, "<ad> [<etiket>]" ile kendi etiketinde ayrıştırılmış yeni bir
// tema açar (bkz. upsertThemeForPost).
func GroupThemes(ctx context.Context, st *store.Store, chat llm.Chat) (int, error) {
	analyses, err := st.UnthemedAnalyses(ctx, 1000)
	if err != nil {
		return 0, err
	}
	if len(analyses) == 0 {
		return 0, nil
	}

	bucketOrder, buckets := groupByDomainTag(analyses)

	existingByTag := map[string][]store.Theme{}
	for _, tag := range bucketOrder {
		existing, err := st.ThemesByDomainTag(ctx, tag)
		if err != nil {
			return 0, fmt.Errorf("kova %q mevcut temaları: %w", tag, err)
		}
		existingByTag[tag] = existing
	}

	batches := packBuckets(bucketOrder, buckets)

	linked, clustered, fallback, disambiguated := 0, 0, 0, 0
	for _, batch := range batches {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}

		assignments := clusterBatch(ctx, chat, batch, existingByTag)

		for i, a := range batch.posts {
			tag := batch.postTags[i]
			themeName, ok := assignments[i]
			if !ok || themeName == "" {
				// Eski davranış: kova etiketi doğrudan tema adı.
				themeName = tag
				fallback++
			} else {
				clustered++
			}

			themeID, wasDisambiguated, err := upsertThemeForPost(ctx, st, themeName, tag)
			if err != nil {
				return linked, err
			}
			if wasDisambiguated {
				disambiguated++
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
	log.Printf("themes: %d post temalara bağlandı (%d kova, %d parti/çağrı, %d LLM kümeleme, %d eski davranış, %d ad çakışması ayrıştırıldı)",
		linked, len(bucketOrder), len(batches), clustered, fallback, disambiguated)
	return linked, nil
}

// upsertThemeForPost, bir postu themeName'e DB SINIRINDA güvenle bağlar
// (#149 review bulgusu). theme_name tablo genelinde UNIQUE olduğundan ve
// UpsertTheme çakışmada domain_tag'i güncellemediğinden, themeName aslında
// BAŞKA bir domain_tag'e ait bir temaya işliyor olabilir — bu bellek-içi
// hiçbir haritayla (özellikle partide görünmeyen etiketlerle) tespit
// edilemez. UpsertTheme'in döndürdüğü GERÇEK sahip (ownerDomainTag) tag ile
// eşleşmezse, o temaya BAĞLANMAZ: "<themeName> [<tag>]" adıyla kendi
// etiketinde YENİ bir tema açılır. Bu ayrıştırılmış ad da (teorik olarak,
// örn. iki farklı post aynı ayrıştırılmış adı aynı anda üretirse) başka bir
// etikete aitse, post ESKİ davranışa (domain_tag'i doğrudan tema adı sayma)
// düşer — sonsuz yeniden deneme YOK, en fazla 3 UpsertTheme çağrısı.
//
// disambiguated, bu post için ayrıştırma uygulanıp uygulanmadığını (log
// sayacı için) döner.
func upsertThemeForPost(ctx context.Context, st *store.Store, themeName, tag string) (themeID int64, disambiguated bool, err error) {
	id, owner, err := st.UpsertTheme(ctx, themeName, tag)
	if err != nil {
		return 0, false, err
	}
	if owner == tag {
		return id, false, nil
	}

	// Ad çakışması: bu isim GERÇEKTE başka bir domain_tag'e ait. Kendi
	// etiketiyle ayrıştırılmış ada dene — retheme'nin (#136) temizlemeye
	// çalıştığı türden "theme_name == domain_tag" fallback temaları
	// gereksiz yere yeniden üretmemek için tercih edilen ilk yol budur.
	disambiguatedName := fmt.Sprintf("%s [%s]", themeName, tag)
	id, owner, err = st.UpsertTheme(ctx, disambiguatedName, tag)
	if err != nil {
		return 0, false, err
	}
	if owner == tag {
		return id, true, nil
	}

	// Ayrıştırılmış ad da (teorik olarak) çakıştı — eski davranışa düş.
	id, _, err = st.UpsertTheme(ctx, tag, tag)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
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

// packBuckets, kovaları AÇGÖZLÜ (greedy) şekilde partilere paketler (#149):
// bucketOrder sırasıyla gezilir, kova cari partiye eklenir; eklenince parti
// toplamı themeClusterBucketLimit'i aşacaksa (ve cari parti boş değilse)
// cari parti kapatılır, yeni parti bu kovayla başlar. groupByDomainTag her
// kovayı zaten themeClusterBucketLimit ile sınırladığından tek bir kova
// tek başına her zaman bir partiye sığar (40'lık tek kova kendi partisi
// olur). Bu düzenek, çağrı sayısını gönderi sayısından bağımsız olarak
// mümkün olan en az partiye indirir — davranışı DEĞİŞTİRMEZ, yalnız LLM
// çağrı sayısını düşürür.
func packBuckets(bucketOrder []string, buckets map[string][]store.PostAnalysis) []postBatch {
	var batches []postBatch
	var cur postBatch
	curTotal := 0
	for _, tag := range bucketOrder {
		bucket := buckets[tag]
		if curTotal > 0 && curTotal+len(bucket) > themeClusterBucketLimit {
			batches = append(batches, cur)
			cur = postBatch{}
			curTotal = 0
		}
		cur.tags = append(cur.tags, tag)
		for _, a := range bucket {
			cur.posts = append(cur.posts, a)
			cur.postTags = append(cur.postTags, tag)
		}
		curTotal += len(bucket)
	}
	if curTotal > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// clusterBatch, packBuckets'ın ürettiği TEK parti için TEK LLM çağrısı
// yapar ve parti içindeki her post için (parti-içi 0-bazlı indeks -> tema
// adı) haritası döner (#149). LLM çağrısı hata verirse ya da cevap
// bozuk/boş JSON'sa nil döner (tek satır TR log burada basılır) — çağıran
// GroupThemes her postu ESKİ davranışa (domain_tag) düşürür. Model bir
// postu hiç atamazsa o postun indeksi haritada yer almaz; aynı geri düşüş
// yalnız o post için uygulanır.
//
// NOT (#149 review bulgusu): burada BİLEREK etiketler arası bellek-içi bir
// "themeOwner" kontrolü YOK — böyle bir kontrol yalnız BU PARTİDEKİ
// etiketleri görebilir, partide olmayan bir etiketin mevcut temasıyla
// çakışan bir adı KAÇIRIR (erken ret zararsız ama yanıltıcı bir güvenlik
// hissi verir). Asıl garanti çağıran GroupThemes'te upsertThemeForPost ile
// DB SINIRINDA uygulanır — tüm çakışma türlerini (partide olsun olmasın)
// tek yerden yakalar.
func clusterBatch(ctx context.Context, chat llm.Chat, batch postBatch, existingByTag map[string][]store.Theme) map[int]string {
	user := themeClusterUserPrompt(batch, existingByTag)
	// Yargı çağrısı (tema kümeleme): sıcaklık 0 — tutarlı karar (#106).
	raw, err := chat.ChatJSONWithTemperature(ctx, themeClusterSystem, user, 0)
	if err != nil {
		log.Printf("temalar: parti (%s) için LLM HATA: %v — eski davranışa düşülüyor", strings.Join(batch.tags, ","), err)
		return nil
	}

	assignments, err := parseThemeAssignments(raw, len(batch.posts))
	if err != nil {
		log.Printf("temalar: parti (%s) LLM cevabı bozuk: %v — eski davranışa düşülüyor", strings.Join(batch.tags, ","), err)
		return nil
	}
	return assignments
}

// themeClusterUserPrompt, parti içi kümeleme kullanıcı prompt'unu kurar:
// partideki her etiketin mevcut tema adları (varsa) ayrı ayrı gruplanmış +
// numaralı yeni gönderiler (etiket + başlık + kısa gövde). Numaralandırma
// coherentSubset'teki desenle aynıdır (0-bazlı, batch.posts sırasıyla).
func themeClusterUserPrompt(batch postBatch, existingByTag map[string][]store.Theme) string {
	var sb strings.Builder
	sb.WriteString("Existing themes by tag:\n\n")
	for _, tag := range batch.tags {
		fmt.Fprintf(&sb, "[%s]\n", tag)
		existing := existingByTag[tag]
		if len(existing) == 0 {
			sb.WriteString("(none yet)\n")
		} else {
			for _, t := range existing {
				fmt.Fprintf(&sb, "- %s\n", t.Name)
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("New posts:\n\n")
	for i, a := range batch.posts {
		fmt.Fprintf(&sb, "[%d] (tag: %s) %s\n%s\n\n", i, batch.postTags[i], clip(a.Title, 200), clip(a.Body, 500))
	}
	return sb.String()
}

// parseThemeAssignments, tema kümeleme cevabını savunmacı ayrıştırır. Üst
// seviye JSON parse edilemezse (boş cevap dahil) hata döner — çağıran bunu
// "bozuk JSON" sayıp tüm partiyi eski davranışa düşürür. Geçerli JSON
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
