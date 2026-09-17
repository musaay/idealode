package store

import (
	"context"
	"fmt"
	"testing"
)

// TestThemesReadyForSynthesisClusteredBeatsFrequency, #135'in konusu:
// kümelenmiş (gerçek dert, theme_name != domain_tag) bir tema, DÜŞÜK
// frekansla bile yüksek frekanslı eski etiket temasının (theme_name ==
// domain_tag) ÖNÜNDE sıraya girmeli — preferPayment açık ya da kapalı
// fark etmez (kümelenmiş anahtarı koşulsuz eklenir).
func TestThemesReadyForSynthesisClusteredBeatsFrequency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platformOld := "test-cluster-old"
	platformNew := "test-cluster-new"
	nameOld := "test-cluster-old-tag" // theme_name == domain_tag -> eski tip
	nameNew := "test-cluster-new-theme"
	domainNew := "test-cluster-new-domain"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2)", platformOld, platformNew)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2)", nameOld, nameNew)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupClusterTestTheme(t, ctx, s, platformOld, nameOld, nameOld, 7, false)   // eski tip, yüksek frekans
	setupClusterTestTheme(t, ctx, s, platformNew, nameNew, domainNew, 1, false) // kümelenmiş, düşük frekans

	for _, preferPayment := range []bool{true, false} {
		themes, err := s.ThemesReadyForSynthesis(ctx, 1, 50, preferPayment)
		if err != nil {
			t.Fatalf("ThemesReadyForSynthesis (preferPayment=%v): %v", preferPayment, err)
		}
		idxNew, idxOld := themeIndex(themes, nameNew), themeIndex(themes, nameOld)
		if idxNew < 0 || idxOld < 0 {
			t.Fatalf("preferPayment=%v: her iki tema da dönmeli, geldi: %+v", preferPayment, themes)
		}
		if idxNew >= idxOld {
			t.Errorf("preferPayment=%v: düşük frekanslı kümelenmiş tema (%d) yüksek frekanslı eski tipten (%d) ÖNCE gelmeli", preferPayment, idxNew, idxOld)
		}
		for _, th := range themes {
			switch th.Name {
			case nameNew:
				if !th.Clustered {
					t.Errorf("preferPayment=%v: %s Clustered=true taşımalı", preferPayment, nameNew)
				}
			case nameOld:
				if th.Clustered {
					t.Errorf("preferPayment=%v: %s Clustered=false taşımalı", preferPayment, nameOld)
				}
			}
		}
	}
}

// TestThemesReadyForSynthesisNullDomainTagTreatedAsOld, kritik edge case:
// domain_tag NULL olan bir tema (017 backfill'inden önce/dışında kalmış bir
// satırı simüle eder) düz "IS DISTINCT FROM" ile yanlışlıkla "kümelenmiş"
// sayılabilirdi (NULL'dan farklı her şey true döner) — guard NULL'ı açıkça
// "eski tip" saymalı.
func TestThemesReadyForSynthesisNullDomainTagTreatedAsOld(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platformNull := "test-cluster-nulltag"
	platformNew := "test-cluster-nulltag-new"
	nameNull := "test-cluster-nulltag-theme"
	nameNew := "test-cluster-nulltag-new-theme"
	domainNew := "test-cluster-nulltag-new-domain"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2)", platformNull, platformNew)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2)", nameNull, nameNew)
	}
	cleanup()
	t.Cleanup(cleanup)

	// domain_tag NULL, yüksek frekans (5 post).
	setupClusterTestTheme(t, ctx, s, platformNull, nameNull, "", 5, false)
	// Kümelenmiş, düşük frekans (1 post).
	setupClusterTestTheme(t, ctx, s, platformNew, nameNew, domainNew, 1, false)

	themes, err := s.ThemesReadyForSynthesis(ctx, 1, 50, true)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis: %v", err)
	}

	idxNew, idxNull := themeIndex(themes, nameNew), themeIndex(themes, nameNull)
	if idxNew < 0 || idxNull < 0 {
		t.Fatalf("her iki tema da dönmeli, geldi: %+v", themes)
	}
	if idxNew >= idxNull {
		t.Errorf("kümelenmiş tema (%d) NULL domain_tag'li eski tipten (%d) ÖNCE gelmeli", idxNew, idxNull)
	}
	for _, th := range themes {
		if th.Name == nameNull && th.Clustered {
			t.Errorf("domain_tag NULL olan tema Clustered=false taşımalı (eski tip sayılmalı), geldi: %+v", th)
		}
	}
}

// TestThemesReadyForSynthesisSameTypeOrderPreserved, aynı tipteki temalar
// arasında eski sıralamanın (önce ödeme sinyali, sonra frekans) clustered
// kolonu eklendikten sonra da bozulmadığını doğrular — hem iki kümelenmiş
// tema arasında (ödeme tie-break) hem iki eski tip tema arasında (frekans
// tie-break).
func TestThemesReadyForSynthesisSameTypeOrderPreserved(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platformPayYes := "test-cluster-sametype-pay-yes"
	platformPayNo := "test-cluster-sametype-pay-no"
	namePayYes := "test-cluster-sametype-pay-yes-theme"
	namePayNo := "test-cluster-sametype-pay-no-theme"
	domainPayYes := "test-cluster-sametype-pay-yes-domain"
	domainPayNo := "test-cluster-sametype-pay-no-domain"

	platformOldHi := "test-cluster-sametype-old-hi"
	platformOldLo := "test-cluster-sametype-old-lo"
	nameOldHi := "test-cluster-sametype-old-hi-tag"
	nameOldLo := "test-cluster-sametype-old-lo-tag"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2, $3, $4)",
			platformPayYes, platformPayNo, platformOldHi, platformOldLo)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2, $3, $4)",
			namePayYes, namePayNo, nameOldHi, nameOldLo)
	}
	cleanup()
	t.Cleanup(cleanup)

	// İki kümelenmiş tema, aynı frekans (3), farklı ödeme sinyali.
	setupClusterTestTheme(t, ctx, s, platformPayYes, namePayYes, domainPayYes, 3, true)
	setupClusterTestTheme(t, ctx, s, platformPayNo, namePayNo, domainPayNo, 3, false)
	// İki eski tip tema, farklı frekans.
	setupClusterTestTheme(t, ctx, s, platformOldHi, nameOldHi, nameOldHi, 5, false)
	setupClusterTestTheme(t, ctx, s, platformOldLo, nameOldLo, nameOldLo, 2, false)

	themes, err := s.ThemesReadyForSynthesis(ctx, 1, 50, true)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis: %v", err)
	}

	idxPayYes, idxPayNo := themeIndex(themes, namePayYes), themeIndex(themes, namePayNo)
	if idxPayYes < 0 || idxPayNo < 0 || idxPayYes >= idxPayNo {
		t.Errorf("aynı tipte (kümelenmiş) aynı frekansta ödeme sinyalli tema (%d) sinyalsizden (%d) ÖNCE gelmeli", idxPayYes, idxPayNo)
	}

	idxOldHi, idxOldLo := themeIndex(themes, nameOldHi), themeIndex(themes, nameOldLo)
	if idxOldHi < 0 || idxOldLo < 0 || idxOldHi >= idxOldLo {
		t.Errorf("aynı tipte (eski) yüksek frekanslı tema (%d) düşük frekanslıdan (%d) ÖNCE gelmeli", idxOldHi, idxOldLo)
	}

	// Kümelenmiş temalar (frekans 3) eski temalardan (frekans 5, 2) ÖNCE
	// gelmeli — clustered anahtarı frequency'den önce karşılaştırılır.
	if idxPayNo >= idxOldHi {
		t.Errorf("kümelenmiş tema (%d, ödeme sinyalsiz) eski en yüksek frekanslı temadan (%d) ÖNCE gelmeli", idxPayNo, idxOldHi)
	}
}

// TestThemesReadyForSynthesisPreferPaymentOffKeepsClusteredKey, kabul
// kriteri 4: preferPayment kapalıyken ödeme anahtarı ORDER BY'a hiç girmez
// (aynı frekanslı temalar arasında ödeme sinyali sırayı etkilemez), ama
// kümelenmiş anahtarı yine eklenir (düşük frekanslı kümelenmiş tema yüksek
// frekanslı eski tipin önünde kalır).
func TestThemesReadyForSynthesisPreferPaymentOffKeepsClusteredKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	platformOld := "test-cluster-offpay-old"
	platformNew := "test-cluster-offpay-new"
	nameOld := "test-cluster-offpay-old-tag"
	nameNew := "test-cluster-offpay-new-theme"
	domainNew := "test-cluster-offpay-new-domain"

	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ($1, $2)", platformOld, platformNew)
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name IN ($1, $2)", nameOld, nameNew)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Eski tip, yüksek frekans + ödeme sinyali VAR (preferPayment açık olsa
	// bunu öne çıkarırdı) — preferPayment burada KAPALI.
	setupClusterTestTheme(t, ctx, s, platformOld, nameOld, nameOld, 6, true)
	// Kümelenmiş, düşük frekans, ödeme sinyali YOK.
	setupClusterTestTheme(t, ctx, s, platformNew, nameNew, domainNew, 1, false)

	themes, err := s.ThemesReadyForSynthesis(ctx, 1, 50, false)
	if err != nil {
		t.Fatalf("ThemesReadyForSynthesis (preferPayment kapalı): %v", err)
	}

	idxNew, idxOld := themeIndex(themes, nameNew), themeIndex(themes, nameOld)
	if idxNew < 0 || idxOld < 0 {
		t.Fatalf("her iki tema da dönmeli, geldi: %+v", themes)
	}
	if idxNew >= idxOld {
		t.Errorf("preferPayment kapalıyken bile kümelenmiş tema (%d) eski tipten (%d) ÖNCE gelmeli", idxNew, idxOld)
	}
	// SELECT'teki has_payment_signal her zaman hesaplanır (yalnız ORDER
	// BY'a preferPayment açıkken girer) — bilgi alanı kaybolmamalı.
	for _, th := range themes {
		if th.Name == nameOld && !th.HasPaymentSignal {
			t.Errorf("eski tip temanın HasPaymentSignal'i true olmalı (SELECT her zaman hesaplar): %+v", th)
		}
	}
}

// setupClusterTestTheme, DB'de verilen sayıda post + analiz + tema bağlantısı
// kurar (RefreshThemeStats ile frequency=postCount olur); theme_name ve
// domain_tag ayrı ayrı verilir ki kümelenmiş (theme_name != domain_tag) ve
// eski tip (theme_name == domain_tag) senaryoları ayrı ayrı kurulabilsin.
// domainTag "" verilirse domain_tag DB'de NULL bırakılır (017 backfill
// öncesi/dışı bir satırı simüle eder — UpsertTheme boş string'i NULL değil
// boş string olarak yazar, bu yüzden NULL için doğrudan SQL gerekir).
func setupClusterTestTheme(t *testing.T, ctx context.Context, s *Store, platform, themeName, domainTag string, postCount int, willing bool) int64 {
	t.Helper()

	posts := make([]RawPost, postCount)
	for i := range posts {
		posts[i] = RawPost{
			Platform: platform, SourceRef: fmt.Sprintf("s%d", i+1), Community: "c",
			Title: "I wish X", Body: fmt.Sprintf("quote %d", i+1),
		}
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
			PostID: id, Classification: "pain_point", DomainTags: []string{themeName},
			WillingnessToPay: willing,
		})
	}
	if err := s.InsertPostAnalyses(ctx, analyses); err != nil {
		t.Fatal(err)
	}

	var themeID int64
	if domainTag == "" {
		if err := s.Pool.QueryRow(ctx, `
			INSERT INTO themes (theme_name, domain_tag) VALUES ($1, NULL)
			ON CONFLICT (theme_name) DO UPDATE SET last_seen = now()
			RETURNING id`, themeName).Scan(&themeID); err != nil {
			t.Fatal(err)
		}
	} else {
		id, _, err := s.UpsertTheme(ctx, themeName, domainTag)
		if err != nil {
			t.Fatal(err)
		}
		themeID = id
	}
	for _, id := range postIDs {
		if err := s.LinkThemePost(ctx, themeID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RefreshThemeStats(ctx); err != nil {
		t.Fatal(err)
	}
	return themeID
}
