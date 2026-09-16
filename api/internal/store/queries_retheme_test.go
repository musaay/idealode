package store

import (
	"context"
	"testing"
	"time"
)

// setupRethemeIdea, verilen temaya bağlı (source_theme_id) minimal bir
// pain_point kartı yazar — "kartlı tema asla dokunulmaz" senaryosunu kurmak
// için (#136).
func setupRethemeIdea(t *testing.T, ctx context.Context, s *Store, themeID int64, title string) int64 {
	t.Helper()
	id, err := s.InsertIdea(ctx, Idea{
		Title:            title,
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType:   "pain_point",
		UrgencyScore: 3,
		SourceThemeID: func() *int64 {
			v := themeID
			return &v
		}(),
	})
	if err != nil {
		t.Fatalf("setupRethemeIdea: %v", err)
	}
	return id
}

// TestRethemeCandidatesSelectsOnlyOldTypeAboveThresholdWithoutIdea, hedef
// küme ayrımının dört senaryoyu doğru ayırdığını doğrular: eski tip+eşik
// üstü+kartsız (HEDEF), yeni tip/kümelenmiş (HARİÇ), eşik altı eski tip
// (HARİÇ), kartlı eski tip (HARİÇ, #136 "eleştirenlere dokunulmaz").
func TestRethemeCandidatesSelectsOnlyOldTypeAboveThresholdWithoutIdea(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platOld := "test-retheme-old"
	platNew := "test-retheme-new"
	platBelow := "test-retheme-below"
	platCarded := "test-retheme-carded"
	nameOld := "test-retheme-old-tag"
	nameNew := "test-retheme-new-theme"
	domainNew := "test-retheme-new-domain"
	nameBelow := "test-retheme-below-tag"
	nameCarded := "test-retheme-carded-tag"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = 'retheme-carded-idea'")
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2, $3, $4)", platOld, platNew, platBelow, platCarded)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2, $3, $4)", nameOld, nameNew, nameBelow, nameCarded)
	}
	cleanup()
	t.Cleanup(cleanup)

	themeOldID := setupClusterTestTheme(t, ctx, s, platOld, nameOld, nameOld, 3, false)
	setupClusterTestTheme(t, ctx, s, platNew, nameNew, domainNew, 3, false)
	setupClusterTestTheme(t, ctx, s, platBelow, nameBelow, nameBelow, 2, false)
	themeCardedID := setupClusterTestTheme(t, ctx, s, platCarded, nameCarded, nameCarded, 3, false)
	setupRethemeIdea(t, ctx, s, themeCardedID, "retheme-carded-idea")

	targets, err := s.RethemeCandidates(ctx, 3, 100)
	if err != nil {
		t.Fatalf("RethemeCandidates: %v", err)
	}

	got := map[int64]bool{}
	for _, tgt := range targets {
		got[tgt.ThemeID] = true
	}
	if !got[themeOldID] {
		t.Errorf("eski tip + eşik üstü + kartsız tema hedef kümede OLMALI: %+v", targets)
	}
	if got[themeCardedID] {
		t.Errorf("kartlı tema hedef kümede OLMAMALI (dokunulmaz kural): %+v", targets)
	}
	for _, tgt := range targets {
		if tgt.ThemeName == nameNew {
			t.Errorf("yeni tip (kümelenmiş) tema hedef kümede OLMAMALI: %+v", targets)
		}
		if tgt.ThemeName == nameBelow {
			t.Errorf("eşik altı eski tip tema hedef kümede OLMAMALI: %+v", targets)
		}
	}
}

// TestRethemeCandidatesLimitAppliesAtPostLevelInDeterministicOrder, --limit
// N'in tema değil GÖNDERİ bazında uygulandığını ve seçim sırasının (tema id,
// post id) tekrar çalıştırmalarda aynı kaldığını doğrular.
func TestRethemeCandidatesLimitAppliesAtPostLevelInDeterministicOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plat := "test-retheme-limit"
	name := "test-retheme-limit-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", plat)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", name)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupClusterTestTheme(t, ctx, s, plat, name, name, 5, false)

	targets, err := s.RethemeCandidates(ctx, 1, 3)
	if err != nil {
		t.Fatalf("RethemeCandidates: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("limit=3 tam olarak 3 GÖNDERİ dönmeli, geldi: %d", len(targets))
	}

	targets2, err := s.RethemeCandidates(ctx, 1, 3)
	if err != nil {
		t.Fatalf("RethemeCandidates (tekrar): %v", err)
	}
	for i := range targets {
		if targets[i].PostID != targets2[i].PostID {
			t.Errorf("seçim sırası tekrar çalıştırmada aynı kalmalı: %d != %d (indeks %d)", targets[i].PostID, targets2[i].PostID, i)
		}
	}
	for i := 1; i < len(targets); i++ {
		if targets[i-1].PostID > targets[i].PostID {
			t.Errorf("post id artan sırada olmalı (tek tema, tek kova): %+v", targets)
		}
	}
}

// TestRethemeCandidateCountMatchesUnlimitedTargetSetSize, limit'siz toplam
// sayımın (RethemeCandidateCount) limitli aday listesiyle (RethemeCandidates,
// büyük limit) tutarlı olduğunu doğrular — "kalan" hesaplamasının temeli.
func TestRethemeCandidateCountMatchesUnlimitedTargetSetSize(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plat := "test-retheme-count"
	name := "test-retheme-count-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", plat)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", name)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupClusterTestTheme(t, ctx, s, plat, name, name, 4, false)

	total, err := s.RethemeCandidateCount(ctx, 1)
	if err != nil {
		t.Fatalf("RethemeCandidateCount: %v", err)
	}
	targets, err := s.RethemeCandidates(ctx, 1, 1000)
	if err != nil {
		t.Fatalf("RethemeCandidates: %v", err)
	}
	if total != len(targets) {
		t.Errorf("toplam sayım (%d) limitsiz aday listesiyle (%d) eşleşmeli", total, len(targets))
	}
	if total != 4 {
		t.Errorf("4 hedef bekleniyordu, geldi: %d", total)
	}
}

// TestRethemeResolveDeletesExactlyLimitAndRefreshesStats, RethemeResolve'un
// tam olarak `limit` gönderiyi theme_posts'tan sildiğini, temayı SİLMEDİĞİNİ
// ve frequency'yi (kalan post sayısına) tazelediğini doğrular — kabul
// kriteri 2 ve 5'in yazan-yol tarafı.
func TestRethemeResolveDeletesExactlyLimitAndRefreshesStats(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plat := "test-retheme-resolve"
	name := "test-retheme-resolve-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", plat)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", name)
	}
	cleanup()
	t.Cleanup(cleanup)

	themeID := setupClusterTestTheme(t, ctx, s, plat, name, name, 5, false)

	resolved, themes, err := s.RethemeResolve(ctx, 1, 2)
	if err != nil {
		t.Fatalf("RethemeResolve: %v", err)
	}
	if resolved != 2 {
		t.Errorf("2 gönderi çözülmeliydi, geldi: %d", resolved)
	}
	if themes != 1 {
		t.Errorf("1 FARKLI tema etkilenmeliydi, geldi: %d", themes)
	}

	var remainingLinks, freq int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM theme_posts WHERE theme_id = $1", themeID).Scan(&remainingLinks); err != nil {
		t.Fatal(err)
	}
	if remainingLinks != 3 {
		t.Errorf("theme_posts'ta 3 bağ kalmalıydı (5-2), geldi: %d", remainingLinks)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT frequency FROM themes WHERE id = $1", themeID).Scan(&freq); err != nil {
		t.Fatal(err)
	}
	if freq != 3 {
		t.Errorf("frequency kalan bağ sayısıyla (3) tazelenmeliydi, geldi: %d", freq)
	}

	var themeStillExists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM themes WHERE id = $1)", themeID).Scan(&themeStillExists); err != nil {
		t.Fatal(err)
	}
	if !themeStillExists {
		t.Error("eski tema satırı SİLİNMEMELİ")
	}
}

// TestRethemeResolveZeroesFrequencyWhenAllPostsResolved, bir temanın TÜM
// post'ları çözüldüğünde frequency'nin 0'a düştüğünü doğrular — bu, eski
// (INNER JOIN'lü) RefreshThemeStats sorgusunun kaçırdığı edge case'ti
// (temanın count subquery'sinde satırı kalmayınca WHERE hiç eşleşmiyordu).
func TestRethemeResolveZeroesFrequencyWhenAllPostsResolved(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plat := "test-retheme-zero"
	name := "test-retheme-zero-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", plat)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", name)
	}
	cleanup()
	t.Cleanup(cleanup)

	themeID := setupClusterTestTheme(t, ctx, s, plat, name, name, 2, false)

	resolved, _, err := s.RethemeResolve(ctx, 1, 10)
	if err != nil {
		t.Fatalf("RethemeResolve: %v", err)
	}
	if resolved != 2 {
		t.Fatalf("2 gönderi çözülmeliydi, geldi: %d", resolved)
	}

	var freq int
	if err := s.Pool.QueryRow(ctx, "SELECT frequency FROM themes WHERE id = $1", themeID).Scan(&freq); err != nil {
		t.Fatal(err)
	}
	if freq != 0 {
		t.Errorf("tüm post'lar çözülünce frequency 0'a düşmeliydi, geldi: %d", freq)
	}
}

// TestRethemeResolveEmptyTargetSetIsIdempotent, hedef küme boşken
// RethemeResolve'un hata vermediğini ve 0/0 döndüğünü doğrular (spec
// idempotency kuralı).
func TestRethemeResolveEmptyTargetSetIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Var olmayan çok yüksek bir eşikle hedef küme kesin boş olur.
	resolved, themes, err := s.RethemeResolve(ctx, 1_000_000_000, 10)
	if err != nil {
		t.Fatalf("boş hedef kümede hata BEKLENMİYORDU: %v", err)
	}
	if resolved != 0 || themes != 0 {
		t.Errorf("boş hedef kümede 0/0 beklenirdi, geldi: resolved=%d themes=%d", resolved, themes)
	}
}

// TestRethemeResolveAtomicOnFailure, transaction yarıda kesilirse (burada:
// refreshThemeStats adımı ayrı bir bağlantının tuttuğu satır kilidine
// takılıp ctx zaman aşımıyla hataya düşer) daha önce batch'te silinen
// theme_posts satırlarının GERİ ALINDIĞINI (ROLLBACK) doğrular — kabul
// kriteri 5.
func TestRethemeResolveAtomicOnFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	plat := "test-retheme-atomic"
	name := "test-retheme-atomic-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", plat)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", name)
	}
	cleanup()
	t.Cleanup(cleanup)

	themeID := setupClusterTestTheme(t, ctx, s, plat, name, name, 1, false)

	// Ayrı bir transaction'da tema satırını kilitle, COMMIT/ROLLBACK ETME —
	// RethemeResolve'un refreshThemeStats'ı TÜM themes satırlarını
	// güncellemeye çalıştığından bu satırda takılıp kalacak.
	lockTx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("kilit tx başlatılamadı: %v", err)
	}
	if _, err := lockTx.Exec(ctx, "SELECT id FROM themes WHERE id = $1 FOR UPDATE", themeID); err != nil {
		t.Fatalf("satır kilidi alınamadı: %v", err)
	}

	shortCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()

	resolved, themes, resolveErr := s.RethemeResolve(shortCtx, 1, 10)

	// Kilidi bırak (test temizliği) — resolveErr kontrolünden SONRA, DB'nin
	// asıl (arka planda kalmış) durumunu ana ctx ile denetleyebilelim.
	_ = lockTx.Rollback(ctx)

	if resolveErr == nil {
		t.Fatalf("kilitli satır nedeniyle hata BEKLENİYORDU, geldi: resolved=%d themes=%d", resolved, themes)
	}

	var remainingLinks int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM theme_posts WHERE theme_id = $1", themeID).Scan(&remainingLinks); err != nil {
		t.Fatal(err)
	}
	if remainingLinks != 1 {
		t.Errorf("hata sonrası ROLLBACK ile bağ SİLİNMEMİŞ kalmalıydı, geldi: %d satır", remainingLinks)
	}
}

// TestRethemeWontReclusterCount, çözüldükten sonra UnthemedAnalyses
// kriterini SAĞLAMAYACAK (noise sınıflandırması) bir post'un doğru
// sayıldığını, sağlayan (pain_point + dolu domain_tags) bir post'un
// sayılmadığını doğrular (dry-run raporu, spec edge case).
func TestRethemeWontReclusterCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test-retheme-wontrecluster", SourceRef: "ok", Community: "c", Title: "ok"},
		{Platform: "test-retheme-wontrecluster", SourceRef: "noise", Community: "c", Title: "noise"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-retheme-wontrecluster'")
	})
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatal(err)
	}
	var okID, noiseID int64
	if err := s.Pool.QueryRow(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-retheme-wontrecluster' AND source_ref = 'ok'").Scan(&okID); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-retheme-wontrecluster' AND source_ref = 'noise'").Scan(&noiseID); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPostAnalyses(ctx, []PostAnalysis{
		{PostID: okID, Classification: "pain_point", DomainTags: []string{"test-retheme-wontrecluster-tag"}},
		{PostID: noiseID, Classification: "noise", DomainTags: []string{"test-retheme-wontrecluster-tag"}},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.RethemeWontReclusterCount(ctx, []int64{okID, noiseID})
	if err != nil {
		t.Fatalf("RethemeWontReclusterCount: %v", err)
	}
	if n != 1 {
		t.Errorf("yalnız noise post yeniden kümelenemeyecek sayılmalıydı (1), geldi: %d", n)
	}
}

// TestRethemeWontReclusterCountEmptyInput, boş post id listesinde sorgu
// çalıştırmadan 0 döndüğünü doğrular (savunmacı guard).
func TestRethemeWontReclusterCountEmptyInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	n, err := s.RethemeWontReclusterCount(ctx, nil)
	if err != nil {
		t.Fatalf("boş girişte hata BEKLENMİYORDU: %v", err)
	}
	if n != 0 {
		t.Errorf("boş girişte 0 beklenirdi, geldi: %d", n)
	}
}
