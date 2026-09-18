package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// themeFallbackChat, tema kümeleme çağrısını HER ZAMAN hata ile başarısız
// kılan sahte chat — GroupThemes'i eski (domain_tag'e bağlama) davranışına
// zorlar. Kümeleme dışı testlerde (bu dosyadaki idempotency testi,
// synthesize_test.go'daki sentez testleri) tema adının domain_tag ile
// birebir aynı kalmasını garanti eder — mevcut assertion'lar bozulmaz.
type themeFallbackChat struct{}

func (themeFallbackChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return "", fmt.Errorf("themeFallbackChat: kullanılmamalı")
}

func (themeFallbackChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	return "", fmt.Errorf("simulated: LLM kullanılamıyor")
}

// fakeClusterChat, tema kümeleme testleri için sabit bir cevap döner; çağrı
// sayısını ve son sıcaklığı kaydeder (kova başına tek çağrı ve sıcaklık 0
// doğrulamaları için).
type fakeClusterChat struct {
	response string
	calls    int
	lastTemp float64
}

func (f *fakeClusterChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *fakeClusterChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	f.calls++
	f.lastTemp = temp
	return f.response, nil
}

// errClusterChat, LLM hatası simülasyonu (429/ağ) — clusterBatch'ın geri
// düşüş yolunu (nil harita) doğrulamak için.
type errClusterChat struct{ calls int }

func (e *errClusterChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return e.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (e *errClusterChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	e.calls++
	return "", fmt.Errorf("simulated 429")
}

// tooLargeThenOKChat, kullanıcı prompt'undaki gönderi sayısı splitThreshold'u
// AŞARSA llm.IsRequestTooLarge'ın true döneceği bir 413 hatası, aşmazsa
// (parti yeterince küçültülmüşse) her post için bir atama içeren başarılı
// bir cevap döner — clusterBatch'in 413'te bölüp özyinelemeli yeniden
// deneme davranışını (#156) uçtan uca doğrulamak için. Gönderi sayısı,
// prompt'taki "[idx] (tag: ...)" satırlarının sayımıyla çıkarılır (posts
// alanını doğrudan almadan, gerçek prompt üretimini de dolaylı sınamış
// olur).
type tooLargeThenOKChat struct {
	splitThreshold int
	calls          int
}

func (c *tooLargeThenOKChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *tooLargeThenOKChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.calls++
	n := strings.Count(user, "] (tag:")
	if n > c.splitThreshold {
		return "", llm.NewRequestTooLargeError("test-host", "Request too large for model, please reduce your message size")
	}
	var sb strings.Builder
	sb.WriteString(`{"assignments":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"post":%d,"theme":"theme-%d"}`, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String(), nil
}

// jsonValidateFailedThenOKChat, tooLargeThenOKChat'in json_validate_failed
// (#158) karşılığı: kullanıcı prompt'undaki gönderi sayısı splitThreshold'u
// AŞARSA llm.IsJSONValidateFailed'in true döneceği bir 400
// json_validate_failed hatası, aşmazsa (parti yeterince küçültülmüşse) her
// post için bir atama içeren başarılı bir cevap döner — clusterBatch'in
// json_validate_failed'de bölüp özyinelemeli yeniden deneme davranışını
// uçtan uca doğrulamak için.
type jsonValidateFailedThenOKChat struct {
	splitThreshold int
	calls          int
}

func (c *jsonValidateFailedThenOKChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *jsonValidateFailedThenOKChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.calls++
	n := strings.Count(user, "] (tag:")
	if n > c.splitThreshold {
		return "", llm.NewJSONValidateFailedError("test-host", `{"error":{"code":"json_validate_failed"}}`)
	}
	var sb strings.Builder
	sb.WriteString(`{"assignments":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"post":%d,"theme":"theme-%d"}`, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String(), nil
}

// other400Chat, json_validate_failed DIŞINDA bir 400 hatası simülasyonu
// (örn. geçersiz parametre) — clusterBatch'in yalnız 413 VE
// json_validate_failed'de böldüğünü, DİĞER 400'lerde bölmediğini
// doğrulamak için (#158 kabul kriteri).
type other400Chat struct{ calls int }

func (o *other400Chat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return o.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (o *other400Chat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	o.calls++
	return "", fmt.Errorf("simulated 400: invalid_request_error")
}

// samplePost, clusterBatch birim testleri için minimal bir post_analysis
// üretir (yalnız Title/Body kullanılır — prompt içeriği).
func samplePost(postID int64, title string) store.PostAnalysis {
	return store.PostAnalysis{PostID: postID, Title: title, Body: "body " + title}
}

// singleTagBatch, TEK domain_tag'li post listesinden postBatch kurar (#149
// öncesi tek-kova clusterBucket testlerinin doğrudan karşılığı; partileme
// (birden çok etiketin tek partide birleşmesi) ayrıca packBuckets ve
// GroupThemes testlerinde doğrulanır).
func singleTagBatch(tag string, posts []store.PostAnalysis) postBatch {
	b := postBatch{tags: []string{tag}}
	for _, p := range posts {
		b.posts = append(b.posts, p)
		b.postTags = append(b.postTags, tag)
	}
	return b
}

// insertPost, DB entegrasyon testleri için tek post + pain_point analiz
// satırı kurar ve post id'sini döner.
func insertPost(t *testing.T, ctx context.Context, st *store.Store, platform, ref, tag string) int64 {
	t.Helper()
	if _, err := st.InsertRawPosts(ctx, []store.RawPost{
		{Platform: platform, SourceRef: ref, Community: "c", Title: "t-" + ref, Body: "body " + ref},
	}); err != nil {
		t.Fatalf("insertPost %s: %v", ref, err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx,
		"SELECT id FROM raw_posts WHERE platform = $1 AND source_ref = $2", platform, ref).Scan(&id); err != nil {
		t.Fatalf("insertPost select %s: %v", ref, err)
	}
	if err := st.InsertPostAnalyses(ctx, []store.PostAnalysis{
		{PostID: id, Classification: "pain_point", DomainTags: []string{tag}},
	}); err != nil {
		t.Fatalf("insertPost analysis %s: %v", ref, err)
	}
	return id
}

// insertLongPost, insertPost'un başlık/gövde ÖZELLEŞTİRİLEBİLEN hali — #157
// testinin tek bir domain_tag'i token bütçesi yüzünden (post SAYISI değil)
// birden çok partiye bölecek kadar uzun gönderiler üretmesi için.
func insertLongPost(t *testing.T, ctx context.Context, st *store.Store, platform, ref, tag, title, body string) int64 {
	t.Helper()
	if _, err := st.InsertRawPosts(ctx, []store.RawPost{
		{Platform: platform, SourceRef: ref, Community: "c", Title: title, Body: body},
	}); err != nil {
		t.Fatalf("insertLongPost %s: %v", ref, err)
	}
	var id int64
	if err := st.Pool.QueryRow(ctx,
		"SELECT id FROM raw_posts WHERE platform = $1 AND source_ref = $2", platform, ref).Scan(&id); err != nil {
		t.Fatalf("insertLongPost select %s: %v", ref, err)
	}
	if err := st.InsertPostAnalyses(ctx, []store.PostAnalysis{
		{PostID: id, Classification: "pain_point", DomainTags: []string{tag}},
	}); err != nil {
		t.Fatalf("insertLongPost analysis %s: %v", ref, err)
	}
	return id
}

// promptCapturingChat, kümeleme çağrılarının user prompt'unu SIRAYLA
// kaydeden ve responses'taki cevapları sırayla döndüren sahte chat (#157) —
// ikinci partinin prompt'unun birinci partide açılan tema adını görüp
// görmediğini doğrulamak için. responses'ı aşan çağrılarda boş atama listesi
// döner (o partideki tüm postlar eski davranışa düşer — teste zarar vermez).
type promptCapturingChat struct {
	prompts   []string
	responses []string
}

func (c *promptCapturingChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *promptCapturingChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	idx := len(c.prompts)
	c.prompts = append(c.prompts, user)
	if idx < len(c.responses) {
		return c.responses[idx], nil
	}
	return `{"assignments":[]}`, nil
}

// Gerçek DB isteyen entegrasyon testi; TEST_DATABASE_URL yoksa atlanır.
// GroupThemes'e themeFallbackChat verilir: bu test kümeleme davranışını
// DEĞİL, kaba kova + eski davranış (domain_tag'e bağlama) + noise
// dışlanmasını doğrular — kümeleme testleri aşağıda ayrı.
func TestGroupThemesIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-theme'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name LIKE 'test-tt-%'")
	}
	cleanup()
	t.Cleanup(cleanup)

	posts := []store.RawPost{
		{Platform: "test-theme", SourceRef: "p1", Community: "c", Title: "a"},
		{Platform: "test-theme", SourceRef: "p2", Community: "c", Title: "b"},
		{Platform: "test-theme", SourceRef: "p3", Community: "c", Title: "c"},
	}
	if _, err := st.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("posts: %v", err)
	}
	var ids []int64
	rows, _ := st.Pool.Query(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-theme' ORDER BY id")
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	analyses := []store.PostAnalysis{
		{PostID: ids[0], Classification: "pain_point", DomainTags: []string{"test-tt-invoice", "extra"}},
		{PostID: ids[1], Classification: "feature_request", DomainTags: []string{"test-tt-invoice"}},
		{PostID: ids[2], Classification: "noise", DomainTags: []string{"test-tt-noise"}},
	}
	if err := st.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatalf("analyses: %v", err)
	}

	linked, err := GroupThemes(ctx, st, themeFallbackChat{})
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != 2 {
		t.Errorf("2 post bağlanmalıydı (noise hariç), geldi: %d", linked)
	}

	var freq int
	if err := st.Pool.QueryRow(ctx,
		"SELECT frequency FROM themes WHERE theme_name = 'test-tt-invoice'").Scan(&freq); err != nil {
		t.Fatalf("tema oluşmamış: %v", err)
	}
	if freq != 2 {
		t.Errorf("frequency theme_posts ile tutarlı olmalı (2), geldi: %d", freq)
	}

	var noiseTheme int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM themes WHERE theme_name = 'test-tt-noise'").Scan(&noiseTheme)
	if noiseTheme != 0 {
		t.Error("noise sınıfı tema oluşturmamalı")
	}

	// idempotency: ikinci koşuda yeni bağlantı yok
	linked2, err := GroupThemes(ctx, st, themeFallbackChat{})
	if err != nil {
		t.Fatalf("GroupThemes (2. koşu): %v", err)
	}
	if linked2 != 0 {
		t.Errorf("ikinci koşu 0 bağlamalı, geldi: %d", linked2)
	}
}

// Gerçek DB isteyen entegrasyon testi; TEST_DATABASE_URL yoksa atlanır.
// TestGroupThemesPropagatesNewThemeAcrossSplitBatches, #157 review bulgusunun
// testi: existingByTag başta DB'den TEK sefer çekiliyor; aynı domain_tag'in
// token bütçesi yüzünden (#156) birden çok partiye bölündüğü durumda, 2.
// parti 1. partinin AÇTIĞI/bağladığı tema adını (DB'ye tekrar gitmeden,
// bellekten) "mevcut tema" olarak görmeli — aksi halde yakın-yinelenen tema
// adı riski doğar. Tek bir domain_tag, post SAYISI (39 < 40 sınırı) DEĞİL
// TOKEN bütçesi yüzünden en az iki partiye bölünecek kadar uzun gövdeli
// gönderilerle kurulur (bkz. TestPackBucketsSplitsLargeBucketByTokenBudget
// ile aynı boyutlandırma).
func TestGroupThemesPropagatesNewThemeAcrossSplitBatches(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const platform = "test-theme-split"
	const tag = "test-split-tag"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	longBody := strings.Repeat("kelime dolgu metni ", 40) // clip(500)'e vurur
	const n = 39                                          // themeClusterBucketLimit (40) altında — yalnız TOKEN bütçesi bölmeli
	for i := 0; i < n; i++ {
		insertLongPost(t, ctx, st, platform, fmt.Sprintf("split-%02d", i), tag,
			fmt.Sprintf("uzun post basligi %d", i), longBody)
	}

	chat := &promptCapturingChat{
		responses: []string{
			`{"assignments":[{"post":0,"theme":"test-split-new-theme"}]}`,
		},
	}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != n {
		t.Fatalf("%d post bağlanmalıydı, geldi: %d", n, linked)
	}
	if len(chat.prompts) < 2 {
		t.Fatalf("bu kova token bütçesi yüzünden en az 2 partiye bölünmeliydi, geldi: %d çağrı", len(chat.prompts))
	}
	if !strings.Contains(chat.prompts[1], "test-split-new-theme") {
		t.Errorf("2. partinin prompt'u 1. partide açılan \"test-split-new-theme\" temasını GÖRMELİYDİ (existingByTag bellekte güncellenmeli), prompt:\n%s", chat.prompts[1])
	}
}

// ------------------------------------------------------- clusterBatch (DB'siz)

func TestClusterBatchUsesTemperatureZero(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export data"}]}`}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	clusterBatch(context.Background(), chat, batch, nil)
	if chat.lastTemp != 0 {
		t.Errorf("kümeleme çağrısı sıcaklık 0 ile gitmeli, geldi: %v", chat.lastTemp)
	}
}

// TestClusterBatchSingleCallEvenAtBucketLimit, 40 postluk (tam sınır) tek
// etiketli bir partide bile TEK LLM çağrısı yapıldığını doğrular (#127,
// #149).
func TestClusterBatchSingleCallEvenAtBucketLimit(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[]}`}
	posts := make([]store.PostAnalysis, themeClusterBucketLimit)
	for i := range posts {
		posts[i] = samplePost(int64(i), fmt.Sprintf("post-%d", i))
	}
	batch := singleTagBatch("x", posts)
	clusterBatch(context.Background(), chat, batch, nil)
	if chat.calls != 1 {
		t.Errorf("parti başına TEK çağrı beklenirdi (posts=%d), geldi: %d", len(posts), chat.calls)
	}
}

func TestClusterBatchFallbackOnLLMError(t *testing.T) {
	chat := &errClusterChat{}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, nil)
	if assignments != nil {
		t.Errorf("LLM hatasında nil harita (tam geri düşüş) beklenirdi, geldi: %v", assignments)
	}
	if chat.calls != 1 {
		t.Errorf("yine de TEK çağrı denenmeliydi, geldi: %d", chat.calls)
	}
}

func TestClusterBatchFallbackOnGarbageJSON(t *testing.T) {
	chat := &fakeClusterChat{response: "not json"}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, nil)
	if assignments != nil {
		t.Errorf("bozuk JSON'da nil harita beklenirdi, geldi: %v", assignments)
	}
}

func TestClusterBatchFallbackOnEmptyResponse(t *testing.T) {
	chat := &fakeClusterChat{response: ""}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, nil)
	if assignments != nil {
		t.Errorf("boş cevapta nil harita beklenirdi, geldi: %v", assignments)
	}
}

func TestClusterBatchAssignsToExistingTheme(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export chat history"}]}`}
	existingByTag := map[string][]store.Theme{"x": {{ID: 1, Name: "cannot export chat history"}}}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, existingByTag)
	if assignments[0] != "cannot export chat history" {
		t.Errorf("mevcut temaya atama beklenirdi, geldi: %v", assignments)
	}
}

// TestClusterBatchGeneratesNewTheme, modelin existing listesinde OLMAYAN
// bir ad döndüğünde bunun hata sayılmadığını, doğrudan atama olarak kabul
// edildiğini doğrular (yeni tema olarak upsert edilecek — çağıran katman).
func TestClusterBatchGeneratesNewTheme(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"no bulk invoice download"}]}`}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, nil)
	if assignments[0] != "no bulk invoice download" {
		t.Errorf("yeni tema adı doğrudan kabul edilmeliydi, geldi: %v", assignments)
	}
}

// TestClusterBatchPartialAssignmentFallsBackPerPost, modelin bir postu
// atlamasının YALNIZ o postu etkilediğini doğrular (#127: "model bir
// gönderiyi atamazsa eski davranışa düşülür" — partinin tamamı değil).
func TestClusterBatchPartialAssignmentFallsBackPerPost(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export chat history"}]}`}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a"), samplePost(2, "b")})
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, nil)
	if assignments[0] != "cannot export chat history" {
		t.Errorf("post 0 atanmalıydı, geldi: %v", assignments)
	}
	if _, ok := assignments[1]; ok {
		t.Errorf("post 1 atanmamalıydı (model atlamıştı), geldi: %v", assignments)
	}
}

// TestClusterBatchDoesNotFilterCrossTagAssignmentsInMemory, #149 review
// bulgusunun bir sonucu: clusterBatch BİLEREK etiketler arası bellek-içi
// bir kontrol yapmaz (böyle bir kontrol yalnız partideki etiketleri
// görebilir, partide olmayan bir etiketle çakışmayı KAÇIRIR — yanıltıcı bir
// güvenlik hissi verir). Model bir postu partideki BAŞKA bir etiketin
// mevcut temasına atarsa bile clusterBatch bu atamayı OLDUĞU GİBİ döner;
// gerçek isolasyon garantisi çağıran GroupThemes'te DB sınırında
// (upsertThemeForPost) uygulanır — bkz. TestGroupThemesCrossTagNameCollision*
// entegrasyon testleri.
func TestClusterBatchDoesNotFilterCrossTagAssignmentsInMemory(t *testing.T) {
	// existing["a"] içindeki "foo" temasının sahibi "a"; post 0 ise "b"
	// etiketinden — model postu "a"nın temasıyla AYNI ada atıyor.
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"foo"}]}`}
	existingByTag := map[string][]store.Theme{
		"a": {{ID: 1, Name: "foo"}},
		"b": {},
	}
	batch := postBatch{
		tags:     []string{"a", "b"},
		posts:    []store.PostAnalysis{samplePost(1, "b-post")},
		postTags: []string{"b"},
	}
	assignments, _, _ := clusterBatch(context.Background(), chat, batch, existingByTag)
	if assignments[0] != "foo" {
		t.Errorf("clusterBatch atamayı süzmemeli (DB sınırı bunu ele alır), geldi: %v", assignments)
	}
}

// TestClusterBatchSplitsOn413AndAssignsAllPosts, #156 kabul kriteri:
// clusterBatch 413 (istek çok büyük) aldığında partiyi ikiye bölüp HER
// YARIYI ayrı ayrı (gerekirse özyinelemeli olarak tekrar) yeniden dener ve
// sonunda TÜM gönderiler LLM kümelemeyle (eski davranışa düşmeden) temaya
// bağlanır.
func TestClusterBatchSplitsOn413AndAssignsAllPosts(t *testing.T) {
	posts := make([]store.PostAnalysis, 10)
	for i := range posts {
		posts[i] = samplePost(int64(i), fmt.Sprintf("post-%d", i))
	}
	batch := singleTagBatch("x", posts)
	// 3'ten fazla post içeren HER çağrı 413 alır — 10 post en az iki bölme
	// turu (10 -> 5+5 -> biri hâlâ >3 ise tekrar) gerektirir.
	chat := &tooLargeThenOKChat{splitThreshold: 3}

	assignments, splits, _ := clusterBatch(context.Background(), chat, batch, nil)
	if splits == 0 {
		t.Fatal("413 sonrası en az bir bölme+yeniden deneme beklenir")
	}
	if len(assignments) != len(posts) {
		t.Fatalf("tüm gönderiler LLM kümelemeyle bağlanmalıydı (eski davranışa düşülmeden), geldi: %d/%d", len(assignments), len(posts))
	}
	for i := range posts {
		if _, ok := assignments[i]; !ok {
			t.Errorf("post %d atanmamış kalmış", i)
		}
	}
}

// TestClusterBatchSingleFallsBackOnPersistent413, tek gönderiye inince bile
// 413 devam ederse (daha fazla bölünemez) o postun ESKİ davranışa
// düşürüldüğünü (nil harita — sonsuz özyineleme YOK) doğrular.
func TestClusterBatchSingleFallsBackOnPersistent413(t *testing.T) {
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	// splitThreshold=0: tek postluk parti bile 413 alır.
	chat := &tooLargeThenOKChat{splitThreshold: 0}

	assignments, splits, _ := clusterBatch(context.Background(), chat, batch, nil)
	if splits != 0 {
		t.Errorf("tek postluk parti daha fazla bölünemez, splits=0 beklenirdi, geldi: %d", splits)
	}
	if assignments != nil {
		t.Errorf("kalıcı 413'te nil harita (eski davranışa düşüş) beklenirdi, geldi: %v", assignments)
	}
}

// TestClusterBatchDoesNotSplitOn429, #156 kabul kriteri: 413 DIŞINDAKİ LLM
// hatalarında (429/ağ/vb.) parti BÖLÜNMEZ — tek çağrı denenir, doğrudan eski
// davranışa düşülür. 429'un backoff/retry'si zaten llm paketinde; pipeline
// bunun üstüne binmemeli.
func TestClusterBatchDoesNotSplitOn429(t *testing.T) {
	chat := &errClusterChat{}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a"), samplePost(2, "b")})

	assignments, splits, _ := clusterBatch(context.Background(), chat, batch, nil)
	if splits != 0 {
		t.Errorf("429/genel hatada bölme yapılmamalı, splits=%d", splits)
	}
	if assignments != nil {
		t.Errorf("nil harita (tam geri düşüş) beklenirdi, geldi: %v", assignments)
	}
	if chat.calls != 1 {
		t.Errorf("TEK çağrı denenmeliydi (parti bölünmedi), geldi: %d", chat.calls)
	}
}

// TestClusterBatchSplitsOnJSONValidateFailedAndAssignsAllPosts, #158 kabul
// kriteri: clusterBatch 400 json_validate_failed aldığında partiyi ikiye
// bölüp HER YARIYI ayrı ayrı (gerekirse özyinelemeli olarak tekrar) yeniden
// dener ve sonunda TÜM gönderiler LLM kümelemeyle (eski davranışa
// düşmeden) temaya bağlanır — 413 sayacı (splits413) ETKİLENMEZ, yalnız
// json sayacı (splitsJSON) artar.
func TestClusterBatchSplitsOnJSONValidateFailedAndAssignsAllPosts(t *testing.T) {
	posts := make([]store.PostAnalysis, 10)
	for i := range posts {
		posts[i] = samplePost(int64(i), fmt.Sprintf("post-%d", i))
	}
	batch := singleTagBatch("x", posts)
	// 3'ten fazla post içeren HER çağrı json_validate_failed alır — 10 post
	// en az iki bölme turu gerektirir.
	chat := &jsonValidateFailedThenOKChat{splitThreshold: 3}

	assignments, splits413, splitsJSON := clusterBatch(context.Background(), chat, batch, nil)
	if splits413 != 0 {
		t.Errorf("json_validate_failed bölünmesi 413 sayacını (splits413) artırmamalı, geldi: %d", splits413)
	}
	if splitsJSON == 0 {
		t.Fatal("json_validate_failed sonrası en az bir bölme+yeniden deneme (splitsJSON) beklenir")
	}
	if len(assignments) != len(posts) {
		t.Fatalf("tüm gönderiler LLM kümelemeyle bağlanmalıydı (eski davranışa düşülmeden), geldi: %d/%d", len(assignments), len(posts))
	}
	for i := range posts {
		if _, ok := assignments[i]; !ok {
			t.Errorf("post %d atanmamış kalmış", i)
		}
	}
}

// TestClusterBatchSingleFallsBackOnPersistentJSONValidateFailed, tek
// gönderiye inince bile json_validate_failed devam ederse (daha fazla
// bölünemez) o postun ESKİ davranışa düşürüldüğünü (nil harita — sonsuz
// özyineleme YOK) doğrular (#158).
func TestClusterBatchSingleFallsBackOnPersistentJSONValidateFailed(t *testing.T) {
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a")})
	// splitThreshold=0: tek postluk parti bile json_validate_failed alır.
	chat := &jsonValidateFailedThenOKChat{splitThreshold: 0}

	assignments, splits413, splitsJSON := clusterBatch(context.Background(), chat, batch, nil)
	if splits413 != 0 || splitsJSON != 0 {
		t.Errorf("tek postluk parti daha fazla bölünemez, splits413=0 splitsJSON=0 beklenirdi, geldi: %d/%d", splits413, splitsJSON)
	}
	if assignments != nil {
		t.Errorf("kalıcı json_validate_failed'de nil harita (eski davranışa düşüş) beklenirdi, geldi: %v", assignments)
	}
}

// TestClusterBatchDoesNotSplitOnOther400, #158 kabul kriteri:
// json_validate_failed DIŞINDAKİ 400 hatalarında parti BÖLÜNMEZ — tek çağrı
// denenir, doğrudan eski davranışa düşülür.
func TestClusterBatchDoesNotSplitOnOther400(t *testing.T) {
	chat := &other400Chat{}
	batch := singleTagBatch("x", []store.PostAnalysis{samplePost(1, "a"), samplePost(2, "b")})

	assignments, splits413, splitsJSON := clusterBatch(context.Background(), chat, batch, nil)
	if splits413 != 0 || splitsJSON != 0 {
		t.Errorf("json_validate_failed DIŞINDAKİ 400'de bölme yapılmamalı, splits413=%d splitsJSON=%d", splits413, splitsJSON)
	}
	if assignments != nil {
		t.Errorf("nil harita (tam geri düşüş) beklenirdi, geldi: %v", assignments)
	}
	if chat.calls != 1 {
		t.Errorf("TEK çağrı denenmeliydi (parti bölünmedi), geldi: %d", chat.calls)
	}
}

// ------------------------------------------------------------ packBuckets (DB'siz)

// buildEqualBuckets, her biri `perTag` post içeren `n` adet farklı etiket
// kovası üretir (order + buckets) — partileme testleri için.
func buildEqualBuckets(n, perTag int) (order []string, buckets map[string][]store.PostAnalysis) {
	buckets = map[string][]store.PostAnalysis{}
	for i := 0; i < n; i++ {
		tag := fmt.Sprintf("tag-%02d", i)
		order = append(order, tag)
		for j := 0; j < perTag; j++ {
			buckets[tag] = append(buckets[tag], samplePost(int64(i*perTag+j), fmt.Sprintf("p-%d-%d", i, j)))
		}
	}
	return order, buckets
}

// TestPackBucketsMergesSmallBucketsUpToLimit, #149 kabul kriteri 1'in
// birim-test karşılığı: 50 adet tek gönderilik kova -> 2 parti (40 + 10).
func TestPackBucketsMergesSmallBucketsUpToLimit(t *testing.T) {
	order, buckets := buildEqualBuckets(50, 1)
	batches := packBuckets(order, buckets)
	if len(batches) != 2 {
		t.Fatalf("2 parti beklenirdi (40+10), geldi: %d", len(batches))
	}
	if len(batches[0].posts) != 40 {
		t.Errorf("ilk parti 40 post içermeliydi, geldi: %d", len(batches[0].posts))
	}
	if len(batches[1].posts) != 10 {
		t.Errorf("ikinci parti 10 post içermeliydi, geldi: %d", len(batches[1].posts))
	}
	total := len(batches[0].posts) + len(batches[1].posts)
	if total != 50 {
		t.Errorf("hiçbir post kaybolmamalı, toplam=%d", total)
	}
}

// TestPackBucketsFullBucketIsOwnBatch, 40'lık tek kovanın kendi partisi
// olduğunu (başka kovayla birleşmediğini) doğrular.
func TestPackBucketsFullBucketIsOwnBatch(t *testing.T) {
	order, buckets := buildEqualBuckets(1, themeClusterBucketLimit)
	batches := packBuckets(order, buckets)
	if len(batches) != 1 {
		t.Fatalf("tek parti beklenirdi, geldi: %d", len(batches))
	}
	if len(batches[0].posts) != themeClusterBucketLimit {
		t.Errorf("parti %d post içermeliydi, geldi: %d", themeClusterBucketLimit, len(batches[0].posts))
	}
}

// TestPackBucketsFullBucketDoesNotMergeWithNext, dolu (40) bir kovanın
// ardından gelen küçük kovanın YENİ bir partide başladığını doğrular.
func TestPackBucketsFullBucketDoesNotMergeWithNext(t *testing.T) {
	order, buckets := buildEqualBuckets(1, themeClusterBucketLimit)
	order2, buckets2 := buildEqualBuckets(1, 3)
	// İkinci kovayı farklı bir etiket adıyla ekle (çakışmasın).
	extraTag := "extra-tag"
	buckets[extraTag] = buckets2[order2[0]]
	order = append(order, extraTag)

	batches := packBuckets(order, buckets)
	if len(batches) != 2 {
		t.Fatalf("2 parti beklenirdi (dolu kova birleşmemeli), geldi: %d", len(batches))
	}
	if len(batches[0].posts) != themeClusterBucketLimit {
		t.Errorf("ilk parti %d post içermeliydi, geldi: %d", themeClusterBucketLimit, len(batches[0].posts))
	}
	if len(batches[1].posts) != 3 {
		t.Errorf("ikinci parti 3 post içermeliydi, geldi: %d", len(batches[1].posts))
	}
}

// TestPackBucketsPreservesPostTagMapping, birleşmiş bir partide her post'un
// KENDİ domain_tag'iyle eşlendiğini (yanlış etikete karışmadığını) doğrular.
func TestPackBucketsPreservesPostTagMapping(t *testing.T) {
	order, buckets := buildEqualBuckets(3, 2)
	batches := packBuckets(order, buckets)
	if len(batches) != 1 {
		t.Fatalf("6 post tek partiye sığmalıydı, geldi: %d parti", len(batches))
	}
	b := batches[0]
	for i, p := range b.posts {
		wantTag := fmt.Sprintf("tag-%02d", p.PostID/2)
		if b.postTags[i] != wantTag {
			t.Errorf("post %d (id=%d) etiketi %q olmalıydı, geldi: %q", i, p.PostID, wantTag, b.postTags[i])
		}
	}
}

// TestPackBucketsSplitsLargeBucketByTokenBudget, #156 kök nedeninin
// (kova YALNIZ post sayısıyla sınırlanıyordu, token boyutuyla değil) birim
// test karşılığı: post sayısı themeClusterBucketLimit'in (40) çok altında
// ama gövdeler uzun olduğundan TOPLAM tahmini token themeClusterTokenBudget'ı
// aşan TEK bir kova, birden çok partiye bölünmeli — hiçbir gönderi
// kaybolmadan ve HİÇBİR partinin tahmini toplamı bütçeyi aşmadan.
func TestPackBucketsSplitsLargeBucketByTokenBudget(t *testing.T) {
	// clip(Body,500) sınırına vuracak kadar uzun gövde: her post ~500 rune
	// gövde + ~30 rune başlık -> tahmini ~155 token/post. 35 post (post
	// sayısı sınırının altında) toplamda ~5400+ token eder, tek partiye
	// (6.026 kalan bütçe) sığmaz çünkü sistem prompt'u zaten pay alıyor —
	// aşağıdaki assert gerçek estimateTokens ile hesaplanıp doğrulanıyor.
	longBody := strings.Repeat("kelime dolgu metni ", 40) // ~760 rune, clip(500) devreye girer
	const n = 39                                          // themeClusterBucketLimit'in (40) altında — post SAYISI sınırı bu kovayı bölmez
	posts := make([]store.PostAnalysis, n)
	for i := range posts {
		posts[i] = store.PostAnalysis{PostID: int64(i), Title: fmt.Sprintf("uzun post basligi %d", i), Body: longBody}
	}
	order := []string{"big-tag"}
	buckets := map[string][]store.PostAnalysis{"big-tag": posts}

	// Önkoşul: bu kova tek başına bütçeyi gerçekten aşıyor (yoksa test
	// hiçbir şey kanıtlamaz).
	total := themeClusterSystemTokens
	for i, a := range posts {
		total += estimateTokens(postPromptLine(i, "big-tag", a))
	}
	if total <= themeClusterTokenBudget {
		t.Fatalf("test kovası bütçeyi aşacak şekilde kurulmalıydı, tahmini=%d bütçe=%d", total, themeClusterTokenBudget)
	}
	if n > themeClusterBucketLimit {
		t.Fatalf("test post sayısı sınırın (40) altında kalmalıydı, n=%d", n)
	}

	batches := packBuckets(order, buckets)
	if len(batches) < 2 {
		t.Fatalf("token bütçesini aşan kova (post sayısı sınırın altında olsa da) birden çok partiye bölünmeliydi, geldi: %d parti", len(batches))
	}

	gotTotal := 0
	for bi, b := range batches {
		gotTotal += len(b.posts)
		tokens := themeClusterSystemTokens
		for i, a := range b.posts {
			tokens += estimateTokens(postPromptLine(i, b.postTags[i], a))
		}
		if tokens > themeClusterTokenBudget {
			t.Errorf("parti %d tahmini token bütçesini aşıyor: %d > %d", bi, tokens, themeClusterTokenBudget)
		}
	}
	if gotTotal != n {
		t.Errorf("hiçbir post kaybolmamalı, toplam=%d beklenen=%d", gotTotal, n)
	}
}

// ------------------------------------------------- parseThemeAssignments (DB'siz)

func TestParseThemeAssignmentsFlexibleFormats(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		n    int
		want map[int]string
	}{
		{"int index", `{"assignments":[{"post":0,"theme":"a"}]}`, 2, map[int]string{0: "a"}},
		{"string index", `{"assignments":[{"post":"1","theme":"b"}]}`, 2, map[int]string{1: "b"}},
		{"out of range dropped", `{"assignments":[{"post":5,"theme":"c"}]}`, 2, map[int]string{}},
		{"negative dropped", `{"assignments":[{"post":-1,"theme":"c"}]}`, 2, map[int]string{}},
		{"empty theme dropped", `{"assignments":[{"post":0,"theme":"  "}]}`, 2, map[int]string{}},
		{"duplicate post keeps first", `{"assignments":[{"post":0,"theme":"first"},{"post":0,"theme":"second"}]}`, 2, map[int]string{0: "first"}},
		{"whitespace trimmed", `{"assignments":[{"post":0,"theme":"  padded  "}]}`, 2, map[int]string{0: "padded"}},
		{"empty assignments array", `{"assignments":[]}`, 2, map[int]string{}},
		{"missing assignments key", `{}`, 2, map[int]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseThemeAssignments(tc.raw, tc.n)
			if err != nil {
				t.Fatalf("parseThemeAssignments: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("beklenen %v, geldi %v", tc.want, got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("indeks %d: beklenen %q, geldi %q", k, v, got[k])
				}
			}
		})
	}
}

func TestParseThemeAssignmentsRejectsGarbage(t *testing.T) {
	if _, err := parseThemeAssignments("not json", 2); err == nil {
		t.Error("JSON olmayan cevap hata dönmeli")
	}
	if _, err := parseThemeAssignments("", 2); err == nil {
		t.Error("boş cevap hata dönmeli")
	}
}

// ----------------------------------------------------- groupByDomainTag (DB'siz)

func TestGroupByDomainTagCapsAt40(t *testing.T) {
	var analyses []store.PostAnalysis
	for i := int64(0); i < 45; i++ {
		analyses = append(analyses, store.PostAnalysis{PostID: i, DomainTags: []string{"x"}})
	}
	order, buckets := groupByDomainTag(analyses)
	if len(order) != 1 || order[0] != "x" {
		t.Fatalf("tek kova (x) beklenirdi, geldi: %v", order)
	}
	if len(buckets["x"]) != themeClusterBucketLimit {
		t.Errorf("kova %d ile sınırlanmalıydı, geldi: %d", themeClusterBucketLimit, len(buckets["x"]))
	}
	// İlk 40'ın korunduğunu doğrula (kalan sonraki koşuya kalsın diye).
	if buckets["x"][0].PostID != 0 || buckets["x"][39].PostID != 39 {
		t.Errorf("ilk 40 post korunmalıydı (PostID 0..39), geldi: ilk=%d son=%d",
			buckets["x"][0].PostID, buckets["x"][39].PostID)
	}
}

func TestGroupByDomainTagSkipsEmptyTagsAndPreservesOrder(t *testing.T) {
	analyses := []store.PostAnalysis{
		{PostID: 1, DomainTags: []string{"b"}},
		{PostID: 2, DomainTags: nil},
		{PostID: 3, DomainTags: []string{"a"}},
		{PostID: 4, DomainTags: []string{"b"}},
	}
	order, buckets := groupByDomainTag(analyses)
	if len(order) != 2 || order[0] != "b" || order[1] != "a" {
		t.Errorf("ilk görülme sırası (b, a) beklenirdi, geldi: %v", order)
	}
	if len(buckets["b"]) != 2 {
		t.Errorf("kova b'de 2 post beklenirdi, geldi: %d", len(buckets["b"]))
	}
	if len(buckets["a"]) != 1 {
		t.Errorf("kova a'da 1 post beklenirdi, geldi: %d", len(buckets["a"]))
	}
	total := 0
	for _, b := range buckets {
		total += len(b)
	}
	if total != 3 {
		t.Errorf("etiketsiz post (id=2) hariç tutulmalıydı, toplam=%d", total)
	}
}

// ------------------------------------------------------------ GroupThemes+DB

// TestGroupThemesPacksSmallDifferentTagBucketsIntoOneCall, #149 ile
// DEĞİŞEN davranışı uçtan uca doğrular: 2 FARKLI domain_tag kovası (3 post,
// toplam sınırın altında) artık AYRI çağrılara değil TEK partiye/çağrıya
// paketlenir (eski davranış #127'de "kova başına çağrı"ydı — bu test onun
// yerine geçti). Sıcaklık 0 korunur.
func TestGroupThemesPacksSmallDifferentTagBucketsIntoOneCall(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-cc'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag IN ('test-cc-a', 'test-cc-b')")
	}
	cleanup()
	t.Cleanup(cleanup)

	insertPost(t, ctx, st, "test-cluster-cc", "p1", "test-cc-a")
	insertPost(t, ctx, st, "test-cluster-cc", "p2", "test-cc-a")
	insertPost(t, ctx, st, "test-cluster-cc", "p3", "test-cc-b")

	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"some specific pain"}]}`}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != 3 {
		t.Errorf("3 post bağlanmalıydı, geldi: %d", linked)
	}
	if chat.calls != 1 {
		t.Errorf("2 küçük kova TEK partiye paketlenip TEK çağrı yapmalıydı (#149), geldi: %d", chat.calls)
	}
	if chat.lastTemp != 0 {
		t.Errorf("sıcaklık 0 olmalı, geldi: %v", chat.lastTemp)
	}
}

// TestGroupThemesFiftySingleBucketsTakeTwoCalls, #149 kabul kriteri 1'in
// uçtan uca DB doğrulaması: 50 adet tek gönderilik kova (50 FARKLI
// domain_tag) -> açgözlü paketleme sonucu 2 çağrı (40 + 10). Hiçbir post
// kaybolmaz.
func TestGroupThemesFiftySingleBucketsTakeTwoCalls(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-50'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag LIKE 'test-cc-50-%'")
	}
	cleanup()
	t.Cleanup(cleanup)

	for i := 0; i < 50; i++ {
		insertPost(t, ctx, st, "test-cluster-50", fmt.Sprintf("s%d", i), fmt.Sprintf("test-cc-50-%02d", i))
	}

	chat := &fakeClusterChat{response: `{"assignments":[]}`}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != 50 {
		t.Errorf("50 post bağlanmalıydı, geldi: %d", linked)
	}
	if chat.calls != 2 {
		t.Errorf("50 tek gönderilik kova -> 2 çağrı (40+10) beklenirdi, geldi: %d", chat.calls)
	}
}

// TestGroupThemesLLMClusterReusesThemeAcrossRuns, aynı derdin ikinci koşuda
// MEVCUT temaya atandığını doğrular (#127 kabul kriteri 2): yeni tema
// açılmaz, frequency birikir.
func TestGroupThemesLLMClusterReusesThemeAcrossRuns(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const tag = "test-cc-reuse"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-reuse'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	insertPost(t, ctx, st, "test-cluster-reuse", "r1", tag)

	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export chat history"}]}`}
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes (1. koşu): %v", err)
	}

	var count1 int
	var domainTag1 string
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(domain_tag) FROM themes WHERE theme_name = 'cannot export chat history'").
		Scan(&count1, &domainTag1); err != nil {
		t.Fatal(err)
	}
	if count1 != 1 || domainTag1 != tag {
		t.Fatalf("1. koşu sonrası 1 tema (domain_tag=%s) beklenirdi, geldi: count=%d domain_tag=%s", tag, count1, domainTag1)
	}

	// 2. koşu: aynı kovaya YENİ bir post düşer; fake chat AYNI tema adını
	// döner (var olan temayı "gördüğü" senaryosu).
	insertPost(t, ctx, st, "test-cluster-reuse", "r2", tag)
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes (2. koşu): %v", err)
	}

	var count2, freq int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM themes WHERE theme_name = 'cannot export chat history'").Scan(&count2); err != nil {
		t.Fatal(err)
	}
	if count2 != 1 {
		t.Errorf("ikinci koşu YENİ tema açmamalı, tek satır kalmalı; geldi: %d satır", count2)
	}
	if err := st.Pool.QueryRow(ctx,
		"SELECT frequency FROM themes WHERE theme_name = 'cannot export chat history'").Scan(&freq); err != nil {
		t.Fatal(err)
	}
	if freq != 2 {
		t.Errorf("frequency birikmeliydi (2 post aynı temada), geldi: %d", freq)
	}
}

// TestGroupThemesSameNewThemeNameAcrossTagsDisambiguates (#149 review
// bulgusunun düzeltmesi — ÖNCE/SONRA):
//
//   - ÖNCE (bu testin eski hali, "...KeepsFirstDomainTag"): iki farklı
//     etiketten aynı YENİ tema adı gelince tek satır kaldığını, domain_tag'in
//     İLK yazanınki olarak kaldığını doğruluyordu. Bu, aslında YANLIŞ bir
//     davranışı ("ikinci postun gönderisi başka bir etiketin temasına
//     sessizce bağlanması") doğru sayıyordu — reviewer'ın bulduğu asıl
//     kusurdu.
//   - SONRA (bu test): iki kova artık TEK partide birleştiğinden fake chat
//     HER İKİ post'a (indeks 0 ve 1, farklı etiketler) aynı YENİ ad döner.
//     İLK post (tag a) adı alır. İKİNCİ post (tag b), UpsertTheme'in gerçek
//     sahibinin "a" olduğunu görünce KENDİ etiketiyle ayrıştırılmış bir ada
//     ("shared specific pain [test-cc-collide-b]") yönlendirilir — asla
//     "a"nın temasına bağlanmaz. Artık 2 AYRI tema satırı olmalı, her biri
//     KENDİ domain_tag'iyle.
func TestGroupThemesSameNewThemeNameAcrossTagsDisambiguates(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-collide'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name LIKE 'shared specific pain%'")
	}
	cleanup()
	t.Cleanup(cleanup)

	// c1 (tag-a) ÖNCE eklenir -> post_analysis.id küçük -> UnthemedAnalyses
	// (ORDER BY pa.id) kovasını önce işler -> partide de önce gelir.
	insertPost(t, ctx, st, "test-cluster-collide", "c1", "test-cc-collide-a")
	insertPost(t, ctx, st, "test-cluster-collide", "c2", "test-cc-collide-b")

	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"shared specific pain"},{"post":1,"theme":"shared specific pain"}]}`}
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}

	var countOriginal int
	var domainTagOriginal string
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(domain_tag) FROM themes WHERE theme_name = 'shared specific pain'").
		Scan(&countOriginal, &domainTagOriginal); err != nil {
		t.Fatal(err)
	}
	if countOriginal != 1 {
		t.Fatalf("orijinal ad TEK satır olmalıydı (ilk yazan post'un teması), geldi: %d", countOriginal)
	}
	if domainTagOriginal != "test-cc-collide-a" {
		t.Errorf("orijinal temanın domain_tag'i İLK yazan kovanınki (test-cc-collide-a) olmalıydı, geldi: %s", domainTagOriginal)
	}

	var countDisambiguated int
	var domainTagDisambiguated string
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(domain_tag) FROM themes WHERE theme_name = 'shared specific pain [test-cc-collide-b]'").
		Scan(&countDisambiguated, &domainTagDisambiguated); err != nil {
		t.Fatal(err)
	}
	if countDisambiguated != 1 {
		t.Fatalf("çakışan ikinci post AYRIŞTIRILMIŞ adla YENİ bir satır açmalıydı, geldi: %d", countDisambiguated)
	}
	if domainTagDisambiguated != "test-cc-collide-b" {
		t.Errorf("ayrıştırılmış temanın domain_tag'i post'un KENDİ etiketi (test-cc-collide-b) olmalıydı, geldi: %s", domainTagDisambiguated)
	}

	// İkinci post'un ORİJİNAL ("a"ya ait) temaya BAĞLANMADIĞINI doğrula:
	// theme_posts üzerinden orijinal temanın frequency'si yalnız 1 olmalı
	// (c1), c2 disambiguated temaya gitmiş olmalı.
	var freqOriginal int
	if err := st.Pool.QueryRow(ctx,
		"SELECT frequency FROM themes WHERE theme_name = 'shared specific pain'").Scan(&freqOriginal); err != nil {
		t.Fatal(err)
	}
	if freqOriginal != 1 {
		t.Errorf("orijinal temanın frequency'si 1 olmalıydı (yalnız c1), geldi: %d — c2 yanlışlıkla buraya bağlanmış olabilir", freqOriginal)
	}
}

// TestGroupThemesCrossTagCollisionOutsideBatchDisambiguates (#149 review
// bulgusu senaryo (a)): model bir postu "yeni" sanıp bir ad üretir, ama bu
// ad PARTİDE OLMAYAN başka bir etiketin (D) ÖNCEDEN VAR OLAN temasıyla
// aynıdır. Bellek-içi bir kontrol bunu asla göremez (D bu partide yok);
// gerçek garanti DB sınırında (upsertThemeForPost) çalışır: post D'nin
// temasına BAĞLANMAZ, kendi etiketiyle ayrıştırılmış YENİ bir tema açılır
// ve D'nin teması dokunulmadan kalır (frequency artmaz).
func TestGroupThemesCrossTagCollisionOutsideBatchDisambiguates(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const tagD = "test-cc-outside-d"
	const tagX = "test-cc-outside-x"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-outside'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag IN ($1, $2)", tagD, tagX)
	}
	cleanup()
	t.Cleanup(cleanup)

	// D'nin ÖNCEDEN VAR OLAN teması — bu koşuda hiç post getirmeyecek,
	// yalnız "partide olmayan etiket" senaryosunu kurmak için var.
	preexistingThemeID, ownerTag, err := st.UpsertTheme(ctx, "foo", tagD)
	if err != nil {
		t.Fatalf("ön koşul teması: %v", err)
	}
	if ownerTag != tagD {
		t.Fatalf("ön koşul teması kendi etiketiyle açılmalıydı, geldi: %s", ownerTag)
	}

	// Bu koşuda yalnız X etiketinden bir post var — D partide YOK.
	insertPost(t, ctx, st, "test-cluster-outside", "o1", tagX)

	// Model post'u (X'ten) "foo" adına atıyor — kendi bakış açısından bu
	// yeni bir ad (X'in mevcut teması boş), ama "foo" GERÇEKTE D'ye ait.
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"foo"}]}`}
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}

	// D'nin orijinal teması dokunulmamalı: frequency 0 kalmalı (hiç post
	// bağlanmadı).
	var freqD int
	if err := st.Pool.QueryRow(ctx, "SELECT frequency FROM themes WHERE id = $1", preexistingThemeID).Scan(&freqD); err != nil {
		t.Fatal(err)
	}
	if freqD != 0 {
		t.Errorf("D'nin teması dokunulmamalıydı (frequency 0), geldi: %d — X'ten gelen post yanlışlıkla buraya bağlanmış olabilir", freqD)
	}

	// X'in postu AYRIŞTIRILMIŞ adla kendi etiketinde yeni bir temaya
	// bağlanmalı.
	wantName := "foo [" + tagX + "]"
	var countX int
	var domainTagX string
	var freqX int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(domain_tag), max(frequency) FROM themes WHERE theme_name = $1", wantName).
		Scan(&countX, &domainTagX, &freqX); err != nil {
		t.Fatal(err)
	}
	if countX != 1 {
		t.Fatalf("X için ayrıştırılmış tema (%s) TEK satır olmalıydı, geldi: %d", wantName, countX)
	}
	if domainTagX != tagX {
		t.Errorf("ayrıştırılmış temanın domain_tag'i %s olmalıydı, geldi: %s", tagX, domainTagX)
	}
	if freqX != 1 {
		t.Errorf("ayrıştırılmış temanın frequency'si 1 olmalıydı (X'in postu), geldi: %d", freqX)
	}
}

// TestGroupThemesCrossTagCollisionDoesNotReviveIncoherentTheme (#149 review
// bulgusu — 2. tur): UpsertTheme'in eski `ON CONFLICT ... SET last_seen =
// now()` koşulsuz güncellemesi, bir isim çakışmasında (satır BAŞKA bir
// etikete ait) o YABANCI temanın last_seen'ini de tazeliyordu — gönderi o
// temaya bağlanmasa BİLE. ThemesReadyForSynthesis, tutarsızlıktan gömülmüş
// (incoherent_at) bir temayı YALNIZ last_seen > incoherent_at ise "yeni
// kanıt geldi" sayıp yeniden sıraya alıyor; bu yüzden çakışma, hiçbir
// gerçek kanıt olmadan D'nin gömülü temasını canlandırıyordu. Bu test:
//  1. D'nin gömülü teması "foo" bir isim çakışmasından SONRA da last_seen
//     DEĞİŞMEDEN kalır ve ThemesReadyForSynthesis onu hâlâ DÖNDÜRMEZ.
//  2. Negatif kontrol: D'ye GERÇEK (aynı etiketten) yeni kanıt gelince
//     last_seen NORMAL şekilde tazelenir (bugünkü davranış korunuyor).
func TestGroupThemesCrossTagCollisionDoesNotReviveIncoherentTheme(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const tagD = "test-cc-revive-d"
	const tagX = "test-cc-revive-x"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-revive'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag IN ($1, $2)", tagD, tagX)
	}
	cleanup()
	t.Cleanup(cleanup)

	// D'nin ÖNCEDEN VAR OLAN teması "foo": bir post'la kanıtlanmış
	// (frequency=1), sonra tutarsızlıktan gömülmüş (incoherent_at), ve
	// last_seen kasıtlı olarak GEÇMİŞE çekilmiş (last_seen < incoherent_at
	// — "yeni kanıt yok" durumu).
	themeID, ownerTag, err := st.UpsertTheme(ctx, "foo", tagD)
	if err != nil {
		t.Fatalf("ön koşul teması: %v", err)
	}
	if ownerTag != tagD {
		t.Fatalf("ön koşul teması kendi etiketiyle açılmalıydı, geldi: %s", ownerTag)
	}
	dPostID := insertPost(t, ctx, st, "test-cluster-revive", "d1", tagD)
	if err := st.LinkThemePost(ctx, themeID, dPostID); err != nil {
		t.Fatalf("D postu temaya bağlama: %v", err)
	}
	if err := st.RefreshThemeStats(ctx); err != nil {
		t.Fatalf("RefreshThemeStats: %v", err)
	}
	if _, err := st.Pool.Exec(ctx,
		"UPDATE themes SET last_seen = now() - interval '2 hours' WHERE id = $1", themeID); err != nil {
		t.Fatalf("last_seen geçmişe çekme: %v", err)
	}
	if err := st.MarkThemeIncoherent(ctx, themeID); err != nil {
		t.Fatalf("MarkThemeIncoherent: %v", err)
	}

	var lastSeenBefore time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT last_seen FROM themes WHERE id = $1", themeID).Scan(&lastSeenBefore); err != nil {
		t.Fatal(err)
	}

	// Gömülü temanın senteze GİRMEDİĞİNİ baştan doğrula (kurulum doğru mu).
	before, err := st.ThemesReadyForSynthesis(ctx, 1, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (öncesi): %v", err)
	}
	if themeInList(before, themeID) {
		t.Fatalf("kurulum hatası: gömülü tema başlangıçta ThemesReadyForSynthesis'te olmamalıydı")
	}

	// X etiketinden bir post, model tarafından (yanlışlıkla) D'nin "foo"
	// adına atanıyor — isim çakışması, GERÇEK kanıt DEĞİL.
	insertPost(t, ctx, st, "test-cluster-revive", "x1", tagX)
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"foo"}]}`}
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}

	var lastSeenAfterCollision time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT last_seen FROM themes WHERE id = $1", themeID).Scan(&lastSeenAfterCollision); err != nil {
		t.Fatal(err)
	}
	if !lastSeenAfterCollision.Equal(lastSeenBefore) {
		t.Errorf("isim çakışması D'nin temasının last_seen'ini DEĞİŞTİRMEMELİYDİ; önce=%v sonra=%v", lastSeenBefore, lastSeenAfterCollision)
	}

	afterCollision, err := st.ThemesReadyForSynthesis(ctx, 1, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (çakışma sonrası): %v", err)
	}
	if themeInList(afterCollision, themeID) {
		t.Errorf("gömülü tema, sahte last_seen tazelemesiyle senteze YENİDEN GİRMEMELİYDİ")
	}

	// Negatif kontrol: D'ye GERÇEK (aynı etiketten) yeni kanıt gelince
	// last_seen normal şekilde tazelenmeli.
	if _, _, err := st.UpsertTheme(ctx, "foo", tagD); err != nil {
		t.Fatalf("gerçek kanıt upsert: %v", err)
	}
	var lastSeenAfterRealEvidence time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT last_seen FROM themes WHERE id = $1", themeID).Scan(&lastSeenAfterRealEvidence); err != nil {
		t.Fatal(err)
	}
	if !lastSeenAfterRealEvidence.After(lastSeenBefore) {
		t.Errorf("AYNI etiketten gerçek kanıt last_seen'i tazelemeliydi; önce=%v sonra=%v", lastSeenBefore, lastSeenAfterRealEvidence)
	}
}

// themeInList, id'nin themes dilimi içinde olup olmadığını döner (küçük
// test yardımcısı).
func themeInList(themes []store.Theme, id int64) bool {
	for _, t := range themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

// TestGroupThemesSameTagDuplicateNewThemeNoDisambiguation, ayrıştırmanın
// yalnız GERÇEK etiketler arası çakışmada devreye girdiğini, AYNI etiketten
// iki post aynı YENİ adı alınca ayrıştırma OLMADAN normal şekilde AYNI
// temada birleştiğini doğrular (#149 review bulgusu — negatif kontrol).
func TestGroupThemesSameTagDuplicateNewThemeNoDisambiguation(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const tag = "test-cc-sametag-dup"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-sametag-dup'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	insertPost(t, ctx, st, "test-cluster-sametag-dup", "d1", tag)
	insertPost(t, ctx, st, "test-cluster-sametag-dup", "d2", tag)

	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"duplicate new pain"},{"post":1,"theme":"duplicate new pain"}]}`}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}
	if linked != 2 {
		t.Errorf("2 post bağlanmalıydı, geldi: %d", linked)
	}

	var count int
	var freq int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(frequency) FROM themes WHERE theme_name = 'duplicate new pain'").
		Scan(&count, &freq); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("aynı etiketten aynı yeni ad TEK satır olmalıydı (ayrıştırma yok), geldi: %d", count)
	}
	if freq != 2 {
		t.Errorf("her iki post da AYNI temaya bağlanmalıydı (frequency 2), geldi: %d", freq)
	}

	var disambiguatedCount int
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*) FROM themes WHERE theme_name = 'duplicate new pain ["+tag+"]'").Scan(&disambiguatedCount); err != nil {
		t.Fatal(err)
	}
	if disambiguatedCount != 0 {
		t.Errorf("aynı etiket içinde ayrıştırılmış tema OLUŞMAMALIYDI, geldi: %d satır", disambiguatedCount)
	}
}

// TestGroupThemesBucketLimitDefersRestToNextRun, kova başına en fazla 40
// post işlendiğini ve kalanın (burada 5 fazlası) bir sonraki koşuda
// işlendiğini uçtan uca doğrular (#127).
func TestGroupThemesBucketLimitDefersRestToNextRun(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	const tag = "test-cc-limit"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-cluster-limit'")
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	const total = themeClusterBucketLimit + 5
	for i := 0; i < total; i++ {
		insertPost(t, ctx, st, "test-cluster-limit", fmt.Sprintf("lp%d", i), tag)
	}

	// Kümeleme dışı bırakılır (boş assignments) — sayım yalnız kova
	// sınırını ölçsün, isim eşleşmesi karışıklığı olmasın.
	chat := &fakeClusterChat{response: `{"assignments":[]}`}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes (1. koşu): %v", err)
	}
	if linked != themeClusterBucketLimit {
		t.Errorf("1. koşuda en fazla %d post bağlanmalıydı, geldi: %d", themeClusterBucketLimit, linked)
	}
	if chat.calls != 1 {
		t.Errorf("1. koşuda hâlâ tek çağrı beklenirdi (tek kova), geldi: %d", chat.calls)
	}

	// 2. koşu: kalan 5 post işlenmeli.
	linked2, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes (2. koşu): %v", err)
	}
	if linked2 != 5 {
		t.Errorf("2. koşuda kalan 5 post bağlanmalıydı, geldi: %d", linked2)
	}
}
