package store

import (
	"context"
	"fmt"
	"testing"
)

// TestThemesByDomainTagLimitsTo25MostRecent, #149 kabul kriteri 5'i
// doğrular: ThemesByDomainTag etiket başına en fazla 25 tema döner ve
// sıralama last_seen DESC'tir (en son görülen önce, en eskiler dışarıda
// kalır). Kümeleme prompt'una giren "mevcut temalar" bağlamının şişmesini
// önlemek için eklenen sınırdır — limitsiz sürüm #127 incelemesinde takip
// notu olarak işaretlenmişti.
func TestThemesByDomainTagLimitsTo25MostRecent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	tag := "test-tbdt-tag"
	cleanup := func() {
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE domain_tag = $1", tag)
	}
	cleanup()
	t.Cleanup(cleanup)

	const total = 30
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("test-tbdt-theme-%02d", i)
		// last_seen'i geriye doğru azalan sırayla kur: i büyüdükçe
		// last_seen daha YENİ (i=29 en yeni, i=0 en eski).
		if _, err := s.Pool.Exec(ctx,
			`INSERT INTO themes (theme_name, domain_tag, last_seen)
			 VALUES ($1, $2, now() - ($3 * interval '1 second'))`,
			name, tag, total-i); err != nil {
			t.Fatalf("tema kurulumu %s: %v", name, err)
		}
	}

	got, err := s.ThemesByDomainTag(ctx, tag)
	if err != nil {
		t.Fatalf("ThemesByDomainTag: %v", err)
	}
	if len(got) != 25 {
		t.Fatalf("etiket başına en fazla 25 tema beklenirdi, geldi: %d", len(got))
	}
	if got[0].Name != "test-tbdt-theme-29" {
		t.Errorf("en son görülen tema ilk sırada olmalıydı, geldi: %s", got[0].Name)
	}
	if got[len(got)-1].Name != "test-tbdt-theme-05" {
		t.Errorf("25. sıradaki tema en yeni 25'in sonuncusu (theme-05) olmalıydı, geldi: %s", got[len(got)-1].Name)
	}

	// En eski 5 tema (i=0..4) 25 sınırı dışında kalmalı.
	for i := 0; i < 5; i++ {
		excluded := fmt.Sprintf("test-tbdt-theme-%02d", i)
		for _, th := range got {
			if th.Name == excluded {
				t.Errorf("en eski tema %s dönmemeliydi (25 sınırı dışında kalmalıydı)", excluded)
			}
		}
	}
}
