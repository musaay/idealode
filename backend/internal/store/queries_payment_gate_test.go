package store

import (
	"context"
	"testing"
)

// TestThemesReadyForSynthesisPreferPayment, #125'in konusu: preferPayment
// artık SERT ELEMEZ — sinyalsiz tema da döner — yalnız sıralamayı etkiler:
// aynı frekansta ödeme sinyalli tema, sinyalsizden ÖNCE gelir. preferPayment
// kapalıyken sıralama salt frekans/id'ye göre kalır (önceki #121 davranışı
// sert eleme yapıyordu, bu artık geçerli değil).
func TestThemesReadyForSynthesisPreferPayment(t *testing.T) {
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
	// tema B: 3 post, hepsi willingness_to_pay=false — aynı frekans (3)
	setupThemeWithPayment(t, ctx, s, platformNo, tagNo, false)

	// preferPayment açık: ikisi de döner (artık ELENMEZ), ama aynı frekansta
	// sinyalli tema A, sinyalsiz tema B'den ÖNCE gelmeli.
	themesOn, err := s.ThemesReadyForSynthesis(ctx, 3, 50, true)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (preferPayment açık): %v", err)
	}
	if !containsThemeName(themesOn, tagYes) {
		t.Errorf("preferPayment açıkken ödeme sinyalli tema (%s) dönmeli, geldi: %+v", tagYes, themesOn)
	}
	if !containsThemeName(themesOn, tagNo) {
		t.Errorf("preferPayment açıkken ödeme sinyalsiz tema (%s) de dönmeli (artık elenmiyor), geldi: %+v", tagNo, themesOn)
	}
	idxYes, idxNo := themeIndex(themesOn, tagYes), themeIndex(themesOn, tagNo)
	if idxYes < 0 || idxNo < 0 || idxYes >= idxNo {
		t.Errorf("preferPayment açıkken aynı frekansta sinyalli tema (%d) sinyalsizden (%d) ÖNCE gelmeli", idxYes, idxNo)
	}
	for _, th := range themesOn {
		switch th.Name {
		case tagYes:
			if !th.HasPaymentSignal {
				t.Errorf("tema %s HasPaymentSignal=true taşımalı", tagYes)
			}
		case tagNo:
			if th.HasPaymentSignal {
				t.Errorf("tema %s HasPaymentSignal=false taşımalı", tagNo)
			}
		}
	}

	// preferPayment kapalı: eski sıralama — yalnız frekans DESC, id ASC.
	// Aynı frekansta olduklarından sıra id'ye (kayıt sırasına) göre belirlenir.
	themesOff, err := s.ThemesReadyForSynthesis(ctx, 3, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (preferPayment kapalı): %v", err)
	}
	if !containsThemeName(themesOff, tagYes) || !containsThemeName(themesOff, tagNo) {
		t.Errorf("preferPayment kapalıyken her iki tema da dönmeli, geldi: %+v", themesOff)
	}
}

func containsThemeName(themes []Theme, name string) bool {
	return themeIndex(themes, name) >= 0
}

func themeIndex(themes []Theme, name string) int {
	for i, th := range themes {
		if th.Name == name {
			return i
		}
	}
	return -1
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

	themeID, _, err := s.UpsertTheme(ctx, tag, tag)
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
