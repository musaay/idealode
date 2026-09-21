package pipeline

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/musaay/idealode/backend/internal/store"
)

// rethemeTestStore, gerçek DB isteyen retheme testleri için ortak kurulum
// (TEST_DATABASE_URL yoksa atlanır — themes_test.go'daki desenle aynı).
func rethemeTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
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
	return st, ctx
}

// TestRethemeRejectsNonPositiveLimit, --limit ZORUNLU kuralını (0/negatif
// hata) hem yazan (Retheme) hem salt-okunur (RethemeDryRun) yolda doğrular.
func TestRethemeRejectsNonPositiveLimit(t *testing.T) {
	st, ctx := rethemeTestStore(t)

	for _, limit := range []int{0, -1, -100} {
		if _, err := Retheme(ctx, st, 1, limit); err == nil {
			t.Errorf("Retheme limit=%d hata vermeliydi", limit)
		}
		if err := RethemeDryRun(ctx, st, 1, limit); err == nil {
			t.Errorf("RethemeDryRun limit=%d hata vermeliydi", limit)
		}
	}
}

// TestRethemeDryRunWritesNothing, --dry-run'ın theme_posts bağını ve
// frequency'yi HİÇ değiştirmediğini doğrular (kabul kriteri 1).
func TestRethemeDryRunWritesNothing(t *testing.T) {
	st, ctx := rethemeTestStore(t)

	const tag = "test-retheme-dryrun"
	const platform = "test-retheme-dryrun-platform"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	for i := 0; i < 3; i++ {
		insertPost(t, ctx, st, platform, fmt.Sprintf("d%d", i), tag)
	}
	if _, err := GroupThemes(ctx, st, themeFallbackChat{}); err != nil {
		t.Fatalf("GroupThemes (kurulum): %v", err)
	}

	var beforeLinks, beforeFreq int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM theme_posts tp JOIN themes t ON t.id = tp.theme_id WHERE t.domain_tag = $1", tag).Scan(&beforeLinks)
	st.Pool.QueryRow(ctx, "SELECT frequency FROM themes WHERE domain_tag = $1", tag).Scan(&beforeFreq)
	if beforeLinks != 3 {
		t.Fatalf("kurulum: 3 bağ beklenirdi, geldi: %d", beforeLinks)
	}

	if err := RethemeDryRun(ctx, st, 1, 2); err != nil {
		t.Fatalf("RethemeDryRun: %v", err)
	}

	var afterLinks, afterFreq int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM theme_posts tp JOIN themes t ON t.id = tp.theme_id WHERE t.domain_tag = $1", tag).Scan(&afterLinks)
	st.Pool.QueryRow(ctx, "SELECT frequency FROM themes WHERE domain_tag = $1", tag).Scan(&afterFreq)

	if beforeLinks != afterLinks || beforeFreq != afterFreq {
		t.Errorf("dry-run hiçbir şey YAZMAMALIYDI: bağ %d->%d, frequency %d->%d", beforeLinks, afterLinks, beforeFreq, afterFreq)
	}
}

// TestRethemeResolvesPostsAndReportsRemaining, --limit'in tam uygulandığını
// ve "kalan" sayısının doğru raporlandığını uçtan uca doğrular (kabul
// kriteri 2), ardından hedef küme boşalınca idempotent 0 döndüğünü.
func TestRethemeResolvesPostsAndReportsRemaining(t *testing.T) {
	st, ctx := rethemeTestStore(t)

	const tag = "test-retheme-resolve-pipeline"
	const platform = "test-retheme-resolve-pipeline-platform"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	for i := 0; i < 5; i++ {
		insertPost(t, ctx, st, platform, fmt.Sprintf("r%d", i), tag)
	}
	if _, err := GroupThemes(ctx, st, themeFallbackChat{}); err != nil {
		t.Fatalf("GroupThemes (kurulum): %v", err)
	}

	result, err := Retheme(ctx, st, 1, 2)
	if err != nil {
		t.Fatalf("Retheme: %v", err)
	}
	if result.Resolved != 2 {
		t.Errorf("2 gönderi çözülmeliydi, geldi: %d", result.Resolved)
	}
	if result.Themes != 1 {
		t.Errorf("1 FARKLI tema etkilenmeliydi, geldi: %d", result.Themes)
	}
	if result.Remaining != 3 {
		t.Errorf("kalan 3 (5-2) olmalıydı, geldi: %d", result.Remaining)
	}

	result2, err := Retheme(ctx, st, 1, 100)
	if err != nil {
		t.Fatalf("Retheme (2. tur): %v", err)
	}
	if result2.Resolved != 3 || result2.Remaining != 0 {
		t.Errorf("2. turda kalan 3 gönderi çözülüp kalan 0 kalmalıydı, geldi: resolved=%d kalan=%d", result2.Resolved, result2.Remaining)
	}

	// İdempotent: hedef küme artık boş, hata YOK.
	result3, err := Retheme(ctx, st, 1, 10)
	if err != nil {
		t.Fatalf("Retheme (boş hedef küme): %v", err)
	}
	if result3.Resolved != 0 || result3.Themes != 0 || result3.Remaining != 0 {
		t.Errorf("boş hedef kümede 0/0/0 beklenirdi, geldi: %+v", result3)
	}
}

// TestRethemeResolvedPostsReclusterOnNextGroupThemes, #136'nın asıl kabul
// kriteri (4): Retheme'in çözdüğü gönderi, bir sonraki GroupThemes
// çağrısında (fake chat ile) UnthemedAnalyses tarafından temasız bulunup
// YENİDEN kümelenir. Retheme kendisi hiçbir kümeleme kodu içermez — bu test
// tam olarak "yeniden kümelemeyi mevcut hat yapar" tasarımını doğrular.
func TestRethemeResolvedPostsReclusterOnNextGroupThemes(t *testing.T) {
	st, ctx := rethemeTestStore(t)

	const tag = "test-retheme-recluster"
	const platform = "test-retheme-recluster-platform"
	const newThemeName = "newly clustered pain from retheme test"
	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", newThemeName)
	}
	cleanup()
	t.Cleanup(cleanup)

	insertPost(t, ctx, st, platform, "rc1", tag)
	if _, err := GroupThemes(ctx, st, themeFallbackChat{}); err != nil {
		t.Fatalf("GroupThemes (kurulum, eski tip tema): %v", err)
	}

	var linkedBefore bool
	st.Pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM theme_posts tp JOIN themes t ON t.id = tp.theme_id WHERE t.domain_tag = $1)", tag,
	).Scan(&linkedBefore)
	if !linkedBefore {
		t.Fatalf("kurulum: eski tip temaya bağlı post beklenirdi")
	}

	result, err := Retheme(ctx, st, 1, 10)
	if err != nil {
		t.Fatalf("Retheme: %v", err)
	}
	if result.Resolved != 1 {
		t.Fatalf("1 gönderi çözülmeliydi, geldi: %d", result.Resolved)
	}

	// Post artık temasız — ikinci GroupThemes çağrısı onu YENİ bir temaya
	// (fake chat'in döndürdüğü isim) bağlamalı.
	chat := &fakeClusterChat{response: fmt.Sprintf(`{"assignments":[{"post":0,"theme":%q}]}`, newThemeName)}
	linked, err := GroupThemes(ctx, st, chat)
	if err != nil {
		t.Fatalf("GroupThemes (yeniden kümeleme): %v", err)
	}
	if linked != 1 {
		t.Errorf("1 post yeniden kümelenmeliydi, geldi: %d", linked)
	}

	var newThemeCount int
	st.Pool.QueryRow(ctx, "SELECT count(*) FROM themes WHERE theme_name = $1", newThemeName).Scan(&newThemeCount)
	if newThemeCount != 1 {
		t.Errorf("yeni tema oluşmalıydı, geldi: %d satır", newThemeCount)
	}
}
