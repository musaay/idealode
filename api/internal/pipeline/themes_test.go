package pipeline

import (
	"context"
	"fmt"
	"os"
	"testing"

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

// errClusterChat, LLM hatası simülasyonu (429/ağ) — clusterBucket'ın geri
// düşüş yolunu (nil harita) doğrulamak için.
type errClusterChat struct{ calls int }

func (e *errClusterChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return e.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (e *errClusterChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	e.calls++
	return "", fmt.Errorf("simulated 429")
}

// samplePost, clusterBucket birim testleri için minimal bir post_analysis
// üretir (yalnız Title/Body kullanılır — prompt içeriği).
func samplePost(postID int64, title string) store.PostAnalysis {
	return store.PostAnalysis{PostID: postID, Title: title, Body: "body " + title}
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

// ------------------------------------------------------- clusterBucket (DB'siz)

func TestClusterBucketUsesTemperatureZero(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export data"}]}`}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	clusterBucket(context.Background(), chat, "x", nil, bucket)
	if chat.lastTemp != 0 {
		t.Errorf("kümeleme çağrısı sıcaklık 0 ile gitmeli, geldi: %v", chat.lastTemp)
	}
}

// TestClusterBucketSingleCallEvenAtBucketLimit, 40 postluk (tam sınır) bir
// kovada bile TEK LLM çağrısı yapıldığını doğrular (#127).
func TestClusterBucketSingleCallEvenAtBucketLimit(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[]}`}
	bucket := make([]store.PostAnalysis, themeClusterBucketLimit)
	for i := range bucket {
		bucket[i] = samplePost(int64(i), fmt.Sprintf("post-%d", i))
	}
	clusterBucket(context.Background(), chat, "x", nil, bucket)
	if chat.calls != 1 {
		t.Errorf("kova başına TEK çağrı beklenirdi (bucket=%d), geldi: %d", len(bucket), chat.calls)
	}
}

func TestClusterBucketFallbackOnLLMError(t *testing.T) {
	chat := &errClusterChat{}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	assignments := clusterBucket(context.Background(), chat, "x", nil, bucket)
	if assignments != nil {
		t.Errorf("LLM hatasında nil harita (tam geri düşüş) beklenirdi, geldi: %v", assignments)
	}
	if chat.calls != 1 {
		t.Errorf("yine de TEK çağrı denenmeliydi, geldi: %d", chat.calls)
	}
}

func TestClusterBucketFallbackOnGarbageJSON(t *testing.T) {
	chat := &fakeClusterChat{response: "not json"}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	assignments := clusterBucket(context.Background(), chat, "x", nil, bucket)
	if assignments != nil {
		t.Errorf("bozuk JSON'da nil harita beklenirdi, geldi: %v", assignments)
	}
}

func TestClusterBucketFallbackOnEmptyResponse(t *testing.T) {
	chat := &fakeClusterChat{response: ""}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	assignments := clusterBucket(context.Background(), chat, "x", nil, bucket)
	if assignments != nil {
		t.Errorf("boş cevapta nil harita beklenirdi, geldi: %v", assignments)
	}
}

func TestClusterBucketAssignsToExistingTheme(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export chat history"}]}`}
	existing := []store.Theme{{ID: 1, Name: "cannot export chat history"}}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	assignments := clusterBucket(context.Background(), chat, "x", existing, bucket)
	if assignments[0] != "cannot export chat history" {
		t.Errorf("mevcut temaya atama beklenirdi, geldi: %v", assignments)
	}
}

// TestClusterBucketGeneratesNewTheme, modelin existing listesinde OLMAYAN
// bir ad döndüğünde bunun hata sayılmadığını, doğrudan atama olarak kabul
// edildiğini doğrular (yeni tema olarak upsert edilecek — çağıran katman).
func TestClusterBucketGeneratesNewTheme(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"no bulk invoice download"}]}`}
	bucket := []store.PostAnalysis{samplePost(1, "a")}
	assignments := clusterBucket(context.Background(), chat, "x", nil, bucket)
	if assignments[0] != "no bulk invoice download" {
		t.Errorf("yeni tema adı doğrudan kabul edilmeliydi, geldi: %v", assignments)
	}
}

// TestClusterBucketPartialAssignmentFallsBackPerPost, modelin bir postu
// atlamasının YALNIZ o postu etkilediğini doğrular (#127: "model bir
// gönderiyi atamazsa eski davranışa düşülür" — bucket'ın tamamı değil).
func TestClusterBucketPartialAssignmentFallsBackPerPost(t *testing.T) {
	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"cannot export chat history"}]}`}
	bucket := []store.PostAnalysis{samplePost(1, "a"), samplePost(2, "b")}
	assignments := clusterBucket(context.Background(), chat, "x", nil, bucket)
	if assignments[0] != "cannot export chat history" {
		t.Errorf("post 0 atanmalıydı, geldi: %v", assignments)
	}
	if _, ok := assignments[1]; ok {
		t.Errorf("post 1 atanmamalıydı (model atlamıştı), geldi: %v", assignments)
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

// TestGroupThemesLLMClusterCallCountPerBucket, LLM'in kova başına TEK
// çağrıldığını (post sayısına göre değil) ve sıcaklığın 0 olduğunu uçtan
// uca doğrular (#127): 2 FARKLI domain_tag kovası (3 post) -> 2 çağrı.
func TestGroupThemesLLMClusterCallCountPerBucket(t *testing.T) {
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
	if chat.calls != 2 {
		t.Errorf("2 FARKLI kova için 2 LLM çağrısı beklenirdi, geldi: %d", chat.calls)
	}
	if chat.lastTemp != 0 {
		t.Errorf("sıcaklık 0 olmalı, geldi: %v", chat.lastTemp)
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

// TestGroupThemesSameThemeNameAcrossBucketsKeepsFirstDomainTag, iki FARKLI
// kovadan aynı tema adı üretilirse (theme_name UNIQUE) tek satır kaldığını
// ve domain_tag'in İLK yazan kovanınki olarak kaldığını doğrular (#127
// edge case: "çakışmada UPDATE yok").
func TestGroupThemesSameThemeNameAcrossBucketsKeepsFirstDomainTag(t *testing.T) {
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
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = 'shared specific pain'")
	}
	cleanup()
	t.Cleanup(cleanup)

	// c1 (tag-a) ÖNCE eklenir -> post_analysis.id küçük -> UnthemedAnalyses
	// (ORDER BY pa.id) kovasını önce işler.
	insertPost(t, ctx, st, "test-cluster-collide", "c1", "test-cc-collide-a")
	insertPost(t, ctx, st, "test-cluster-collide", "c2", "test-cc-collide-b")

	chat := &fakeClusterChat{response: `{"assignments":[{"post":0,"theme":"shared specific pain"}]}`}
	if _, err := GroupThemes(ctx, st, chat); err != nil {
		t.Fatalf("GroupThemes: %v", err)
	}

	var count int
	var domainTag string
	if err := st.Pool.QueryRow(ctx,
		"SELECT count(*), max(domain_tag) FROM themes WHERE theme_name = 'shared specific pain'").
		Scan(&count, &domainTag); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("aynı tema adı iki kovadan gelse de TEK satır beklenirdi, geldi: %d", count)
	}
	if domainTag != "test-cc-collide-a" {
		t.Errorf("domain_tag İLK yazan kovanınki (test-cc-collide-a) kalmalıydı, geldi: %s", domainTag)
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
