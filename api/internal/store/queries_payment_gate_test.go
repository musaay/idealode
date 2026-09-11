package store

import (
	"context"
	"testing"
)

// TestThemesReadyForSynthesisPaymentGate, #121'in konusu: requirePayment
// açıkken yalnız en az bir postu willingness_to_pay=true olan temaların
// döndüğünü, kapalıyken eski davranışın (ikisi de döner) korunduğunu
// doğrular.
func TestThemesReadyForSynthesisPaymentGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platformYes := "test-paygate-yes"
	platformNo := "test-paygate-no"
	tagYes := "test-paygate-yes-tag"
	tagNo := "test-paygate-no-tag"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2)", platformYes, platformNo)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2)", tagYes, tagNo)
	}
	cleanup()
	t.Cleanup(cleanup)

	// tema A: 3 post, hepsi willingness_to_pay=true
	setupThemeWithPayment(t, ctx, s, platformYes, tagYes, true)
	// tema B: 3 post, hepsi willingness_to_pay=false
	setupThemeWithPayment(t, ctx, s, platformNo, tagNo, false)

	// Kapı açık: yalnız tema A dönmeli
	themesOn, err := s.ThemesReadyForSynthesis(ctx, 3, 50, true)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (kapı açık): %v", err)
	}
	if !containsThemeName(themesOn, tagYes) {
		t.Errorf("kapı açıkken ödeme sinyalli tema (%s) dönmeli, geldi: %+v", tagYes, themesOn)
	}
	if containsThemeName(themesOn, tagNo) {
		t.Errorf("kapı açıkken ödeme sinyalsiz tema (%s) DÖNMEMELİ, geldi: %+v", tagNo, themesOn)
	}

	// Kapı kapalı: eski davranış — ikisi de döner
	themesOff, err := s.ThemesReadyForSynthesis(ctx, 3, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (kapı kapalı): %v", err)
	}
	if !containsThemeName(themesOff, tagYes) || !containsThemeName(themesOff, tagNo) {
		t.Errorf("kapı kapalıyken her iki tema da dönmeli, geldi: %+v", themesOff)
	}

	// CountThemesWithoutPaymentSignal: en az tema B'yi saymalı (mutlak sayı
	// yerine alt sınır kontrol edilir — DB'de başka artık veri olabilir).
	after, err := s.CountThemesWithoutPaymentSignal(ctx, 3)
	if err != nil {
		t.Fatalf("CountThemesWithoutPaymentSignal: %v", err)
	}
	if after < 1 {
		t.Errorf("ödeme sinyalsiz en az 1 tema (tema B) sayılmalı, geldi: %d", after)
	}
}

// setupThemeWithPayment, DB'de willingness_to_pay değeri sabit 3 post +
// analiz + tema bağlantısı kurar (RefreshThemeStats ile frequency=3 olur).
func setupThemeWithPayment(t *testing.T, ctx context.Context, s *Store, platform, tag string, willing bool) {
	t.Helper()

	posts := []RawPost{
		{Platform: platform, SourceRef: "s1", Community: "c", Title: "I wish X", Body: "quote one"},
		{Platform: platform, SourceRef: "s2", Community: "c", Title: "I wish Y", Body: "quote two"},
		{Platform: platform, SourceRef: "s3", Community: "c", Title: "I wish Z", Body: "quote three"},
	}
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Pool.Query(ctx, "SELECT id FROM raw_posts WHERE platform = $1", platform)
	if err != nil {
		t.Fatal(err)
	}
	var postIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		postIDs = append(postIDs, id)
	}
	rows.Close()

	var analyses []PostAnalysis
	for _, id := range postIDs {
		analyses = append(analyses, PostAnalysis{
			PostID: id, Classification: "pain_point", DomainTags: []string{tag},
			WillingnessToPay: willing,
		})
	}
	if err := s.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatal(err)
	}

	themeID, err := s.UpsertTheme(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range postIDs {
		if err := s.LinkThemePost(ctx, themeID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RefreshThemeStats(ctx); err != nil {
		t.Fatal(err)
	}
}

func containsThemeName(themes []Theme, name string) bool {
	for _, th := range themes {
		if th.Name == name {
			return true
		}
	}
	return false
}
