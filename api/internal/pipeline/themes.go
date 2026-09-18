package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

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

// themeClusterTokenBudget, TEK LLM çağrısının (sistem istemi + o partiye
// giren gönderi metinleri) aşmaması istenen tahmini token toplamıdır (#156).
// Groq istek başına 8.000 TPM sınırlıyor; 6.500 kasıtlı olarak bu sınırın
// altında bırakılmış bir GÜVENLİK PAYIdır (tahmin kaba, "mevcut temalar"
// bölümü ve JSON biçimlendirme payı bu sayıma dahil DEĞİL — bkz.
// estimateTokens). Aşan kova, packBuckets tarafından birden çok partiye
// bölünür; buna rağmen 413 alınırsa clusterBatch parti bazında ayrıca böler.
const themeClusterTokenBudget = 6500

// estimateTokens, bir metnin tahmini LLM token sayısını döner — kaba bir
// yaklaşımdır (rune sayısı / 3.5, yukarı yuvarlanır), sağlayıcının gerçek
// tokenizer'ıyla birebir örtüşmez; amaç yalnız partiyi 8K TPM sınırının
// GÜVENLE altında tutacak bir üst sınır tahmini üretmektir (#156).
func estimateTokens(s string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) / 3.5))
}

// themeClusterSystemTokens, sistem prompt'unun tahmini token sayısıdır —
// parti başına sabit bir taban maliyet olarak themeClusterTokenBudget'tan
// düşülür (#156).
var themeClusterSystemTokens = estimateTokens(themeClusterSystem)

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

	// knownThemeNames, existingByTag'teki adların bir tag için hızlı
	// tekrarsızlık kontrolüdür (#156) — aynı ad iki kez eklenmesin diye.
	existingByTag := map[string][]store.Theme{}
	knownThemeNames := map[string]map[string]bool{}
	for _, tag := range bucketOrder {
		existing, err := st.ThemesByDomainTag(ctx, tag)
		if err != nil {
			return 0, fmt.Errorf("kova %q mevcut temaları: %w", tag, err)
		}
		existingByTag[tag] = existing
		names := map[string]bool{}
		for _, t := range existing {
			names[t.Name] = true
		}
		knownThemeNames[tag] = names
	}

	batches := packBuckets(bucketOrder, buckets)

	// tokenSplitBuckets: kova, token bütçesi yüzünden BİRDEN FAZLA partiye
	// dağılmışsa sayılır (#156) — yalnız log için, davranışı etkilemez.
	batchesPerTag := map[string]int{}
	for _, b := range batches {
		for _, tag := range b.tags {
			batchesPerTag[tag]++
		}
	}
	tokenSplitBuckets := 0
	for _, n := range batchesPerTag {
		if n > 1 {
			tokenSplitBuckets++
		}
	}

	linked, clustered, fallback, disambiguated, retrySplits := 0, 0, 0, 0, 0
	for _, batch := range batches {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}

		assignments, splits := clusterBatch(ctx, chat, batch, existingByTag)
		retrySplits += splits

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

			themeID, actualName, wasDisambiguated, err := upsertThemeForPost(ctx, st, themeName, tag)
			if err != nil {
				return linked, err
			}
			if wasDisambiguated {
				disambiguated++
			}
			// existingByTag'i BELLEKTE güncelle (#156): aynı tag'in token
			// bütçesi yüzünden birden çok partiye bölündüğü durumda,
			// SONRAKİ parti bu partide yeni açılan/bağlanan tema adını
			// "mevcut tema" olarak görsün — DB'den yeniden çekmeden, yakın
			// yinelenen tema adı riskini azaltır. ThemesByDomainTag ile
			// başta çekilenler zaten knownThemeNames'te olduğundan burada
			// tekrar eklenmez.
			if !knownThemeNames[tag][actualName] {
				knownThemeNames[tag][actualName] = true
				existingByTag[tag] = append(existingByTag[tag], store.Theme{ID: themeID, Name: actualName})
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
	log.Printf("themes: %d post temalara bağlandı (%d kova, %d parti, %d kova token sınırından bölündü, %d parti 413 nedeniyle bölünüp yeniden denendi, %d LLM kümeleme, %d eski davranış, %d ad çakışması ayrıştırıldı)",
		linked, len(bucketOrder), len(batches), tokenSplitBuckets, retrySplits, clustered, fallback, disambiguated)
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
// sayacı için) döner. actualName, DB'de GERÇEKTEN bu tag altında kaydedilen
// tema adıdır (themeName, disambiguatedName ya da tag'in kendisi olabilir) —
// çağıran GroupThemes bunu existingByTag'i bellekte güncellemek için kullanır
// (#157 — aynı tag'in sonraki bir partisi bu adı "mevcut tema" olarak görsün).
func upsertThemeForPost(ctx context.Context, st *store.Store, themeName, tag string) (themeID int64, actualName string, disambiguated bool, err error) {
	id, owner, err := st.UpsertTheme(ctx, themeName, tag)
	if err != nil {
		return 0, "", false, err
	}
	if owner == tag {
		return id, themeName, false, nil
	}

	// Ad çakışması: bu isim GERÇEKTE başka bir domain_tag'e ait. Kendi
	// etiketiyle ayrıştırılmış ada dene — retheme'nin (#136) temizlemeye
	// çalıştığı türden "theme_name == domain_tag" fallback temaları
	// gereksiz yere yeniden üretmemek için tercih edilen ilk yol budur.
	disambiguatedName := fmt.Sprintf("%s [%s]", themeName, tag)
	id, owner, err = st.UpsertTheme(ctx, disambiguatedName, tag)
	if err != nil {
		return 0, "", false, err
	}
	if owner == tag {
		return id, disambiguatedName, true, nil
	}

	// Ayrıştırılmış ad da (teorik olarak) çakıştı — eski davranışa düş.
	id, _, err = st.UpsertTheme(ctx, tag, tag)
	if err != nil {
		return 0, "", false, err
	}
	return id, tag, true, nil
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

// packBuckets, kovaları AÇGÖZLÜ (greedy) şekilde partilere paketler (#149,
// #156): bucketOrder sırasıyla gezilir, kova cari partiye eklenir; eklenince
// parti toplamı themeClusterBucketLimit'i (post sayısı) YA DA
// themeClusterTokenBudget'ı (tahmini token) aşacaksa (ve cari parti boş
// değilse) cari parti kapatılır, yeni parti bu kovayla başlar.
// groupByDomainTag her kovayı zaten themeClusterBucketLimit ile
// sınırladığından POST SAYISI bakımından tek bir kova her zaman bir partiye
// sığar; ama uzun gönderili bir kova TEK BAŞINA token bütçesini aşabilir
// (#156 — 55 gönderilik parti 8.112 tahmini token istedi, TPM 8.000'i
// aştı) — bu durumda kova önce splitBucketByTokens ile token'a göre alt
// parçalara bölünür, sonra bu parçalar normal greedy paketlemeye girer.
func packBuckets(bucketOrder []string, buckets map[string][]store.PostAnalysis) []postBatch {
	var batches []postBatch
	var cur postBatch
	curTags := map[string]bool{}
	curCount := 0
	curTokens := themeClusterSystemTokens

	flush := func() {
		if curCount > 0 {
			batches = append(batches, cur)
		}
		cur = postBatch{}
		curTags = map[string]bool{}
		curCount = 0
		curTokens = themeClusterSystemTokens
	}

	for _, tag := range bucketOrder {
		for _, chunk := range splitBucketByTokens(tag, buckets[tag]) {
			if curCount > 0 && (curCount+len(chunk.posts) > themeClusterBucketLimit || curTokens+chunk.tokens > themeClusterTokenBudget) {
				flush()
			}
			if !curTags[tag] {
				curTags[tag] = true
				cur.tags = append(cur.tags, tag)
			}
			for _, a := range chunk.posts {
				cur.posts = append(cur.posts, a)
				cur.postTags = append(cur.postTags, tag)
			}
			curCount += len(chunk.posts)
			curTokens += chunk.tokens
		}
	}
	flush()
	return batches
}

// bucketChunk, splitBucketByTokens'ın ürettiği tek bir alt parçadır — posts
// + bu parçanın (sistem prompt'u HARİÇ) tahmini gönderi token toplamı.
type bucketChunk struct {
	posts  []store.PostAnalysis
	tokens int
}

// splitBucketByTokens, TEK bir domain_tag kovasını, her parça (sistem
// prompt'uyla birlikte) themeClusterTokenBudget'ı aşmayacak şekilde ardışık
// alt parçalara böler (#156). Kova zaten themeClusterBucketLimit post sayısı
// altında olsa da (groupByDomainTag garantisi) uzun gövdeli gönderiler
// yüzünden TOKEN bakımından tek parçaya sığmayabilir. Tek bir gönderi başlı
// başına bütçeyi aşsa bile (nadiren) kendi tek-postluk parçasında YALNIZ
// BAŞINA döner — daha fazla bölünemez, en iyi çaba budur; gerçek 413 hâlâ
// gelirse clusterBatch parti bazında ayrıca yeniden dener.
func splitBucketByTokens(tag string, bucket []store.PostAnalysis) []bucketChunk {
	if len(bucket) == 0 {
		return nil
	}
	budget := themeClusterTokenBudget - themeClusterSystemTokens
	var chunks []bucketChunk
	var cur bucketChunk
	for i, a := range bucket {
		t := estimateTokens(postPromptLine(i, tag, a))
		if len(cur.posts) > 0 && cur.tokens+t > budget {
			chunks = append(chunks, cur)
			cur = bucketChunk{}
		}
		cur.posts = append(cur.posts, a)
		cur.tokens += t
	}
	if len(cur.posts) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// clusterBatch, packBuckets'ın ürettiği TEK parti için LLM çağrısı yapar ve
// parti içindeki her post için (parti-içi 0-bazlı indeks -> tema adı)
// haritasını + kaç kez bölünüp yeniden denendiğini (splits, yalnız log için)
// döner (#149, #156). LLM çağrısı hata verirse ya da cevap bozuk/boş JSON'sa
// nil harita döner (tek satır TR log burada basılır) — çağıran GroupThemes
// her postu ESKİ davranışa (domain_tag) düşürür. Model bir postu hiç
// atamazsa o postun indeksi haritada yer almaz; aynı geri düşüş yalnız o
// post için uygulanır.
//
// 413 (istek çok büyük — #156): packBuckets tahmini bir bütçeyle paketler,
// gerçek tokenizer'la birebir örtüşmeyebilir. Sağlayıcı yine de 413
// döndürürse (llm.IsRequestTooLarge) parti İKİYE bölünür, HER YARI ayrı ayrı
// (özyinelemeli) yeniden denenir; tek gönderiye inince artık bölünemez, o
// post ESKİ davranışa düşer (sonsuz özyineleme YOK — üst sınır post sayısı
// kadar derinlik). 429/5xx (kota) BİLEREK bu yola GİRMEZ: llm paketi bunu
// zaten kendi içinde backoff ile yeniden dener; tüm denemeler tükenirse
// buraya normal (bölünmeyen) hata olarak düşer.
//
// NOT (#149 review bulgusu): burada BİLEREK etiketler arası bellek-içi bir
// "themeOwner" kontrolü YOK — böyle bir kontrol yalnız BU PARTİDEKİ
// etiketleri görebilir, partide olmayan bir etiketin mevcut temasıyla
// çakışan bir adı KAÇIRIR (erken ret zararsız ama yanıltıcı bir güvenlik
// hissi verir). Asıl garanti çağıran GroupThemes'te upsertThemeForPost ile
// DB SINIRINDA uygulanır — tüm çakışma türlerini (partide olsun olmasın)
// tek yerden yakalar.
func clusterBatch(ctx context.Context, chat llm.Chat, batch postBatch, existingByTag map[string][]store.Theme) (map[int]string, int) {
	user := themeClusterUserPrompt(batch, existingByTag)
	// Yargı çağrısı (tema kümeleme): sıcaklık 0 — tutarlı karar (#106).
	raw, err := chat.ChatJSONWithTemperature(ctx, themeClusterSystem, user, 0)
	if err != nil {
		if llm.IsRequestTooLarge(err) && len(batch.posts) > 1 {
			left, right := splitPostBatch(batch)
			log.Printf("temalar: parti (%s, %d gönderi) 413 aldı — ikiye bölünüp (%d + %d gönderi) yeniden deneniyor",
				strings.Join(batch.tags, ","), len(batch.posts), len(left.posts), len(right.posts))
			leftAssignments, leftSplits := clusterBatch(ctx, chat, left, existingByTag)
			rightAssignments, rightSplits := clusterBatch(ctx, chat, right, existingByTag)
			merged := map[int]string{}
			for i, name := range leftAssignments {
				merged[i] = name
			}
			offset := len(left.posts)
			for i, name := range rightAssignments {
				merged[offset+i] = name
			}
			return merged, leftSplits + rightSplits + 1
		}
		log.Printf("temalar: parti (%s) için LLM HATA: %v — eski davranışa düşülüyor", strings.Join(batch.tags, ","), err)
		return nil, 0
	}

	assignments, err := parseThemeAssignments(raw, len(batch.posts))
	if err != nil {
		log.Printf("temalar: parti (%s) LLM cevabı bozuk: %v — eski davranışa düşülüyor", strings.Join(batch.tags, ","), err)
		return nil, 0
	}
	return assignments, 0
}

// splitPostBatch, 413 sonrası yeniden deneme için bir partiyi post
// sırasının orta noktasından ikiye böler (#156). tags her yarı için, o
// yarıda GERÇEKTEN yer alan etiketlerle (ilk görülme sırasıyla, tekrarsız)
// yeniden kurulur — themeClusterUserPrompt'un "mevcut temalar" bölümü
// yalnız o yarıda geçen etiketleri listelesin diye.
func splitPostBatch(batch postBatch) (left, right postBatch) {
	mid := len(batch.posts) / 2
	if mid == 0 {
		mid = 1
	}
	return subBatch(batch.posts[:mid], batch.postTags[:mid]), subBatch(batch.posts[mid:], batch.postTags[mid:])
}

func subBatch(posts []store.PostAnalysis, postTags []string) postBatch {
	b := postBatch{posts: posts, postTags: postTags}
	seen := map[string]bool{}
	for _, tag := range postTags {
		if !seen[tag] {
			seen[tag] = true
			b.tags = append(b.tags, tag)
		}
	}
	return b
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
		sb.WriteString(postPromptLine(i, batch.postTags[i], a))
	}
	return sb.String()
}

// postPromptLine, TEK bir gönderinin kümeleme prompt'una eklenen satırını
// üretir — themeClusterUserPrompt (gerçek prompt) ve splitBucketByTokens
// (#156 token tahmini) AYNI biçimi kullanır ki tahmin gerçek prompt
// boyutundan sapmasın.
func postPromptLine(idx int, tag string, a store.PostAnalysis) string {
	return fmt.Sprintf("[%d] (tag: %s) %s\n%s\n\n", idx, tag, clip(a.Title, 200), clip(a.Body, 500))
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
