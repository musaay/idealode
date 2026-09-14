package main

import (
	"strings"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

// sp, testte *string literal üretmek için kısa yardımcı.
func sp(s string) *string { return &s }

// TestPendingIdeaSummary, `run: beklemede …` özetindeki tek kart satırının
// biçimini doğrular (#108): başlık 60 rune'a kırpılır, fail kriter koduyla
// birlikte gösterilir, karar yoksa "[?]". Veri-erişimi kararı (#131) varsa
// AYRI bir `[veri erişimi: ...]` etiketi eklenir, yoksa (NULL) hiç eklenmez.
func TestPendingIdeaSummary(t *testing.T) {
	longTitle := strings.Repeat("ü", 80) // çok baytlı karakter — rune bazlı kırpma sınanır

	cases := []struct {
		name string
		p    store.PendingIdea
		want string
	}{
		{
			name: "fail kriterli",
			p: store.PendingIdea{
				ID: 101, Title: "Ücretsiz Transfer Aracı",
				DistinctivenessVerdict: sp("fail"), DistinctivenessCriterion: sp("K3"),
			},
			want: `101 "Ücretsiz Transfer Aracı" [fail K3]`,
		},
		{
			name: "pass",
			p: store.PendingIdea{
				ID: 98, Title: "Dolibarr Entegrasyonu",
				DistinctivenessVerdict: sp("pass"), DistinctivenessCriterion: sp("none"),
			},
			want: `98 "Dolibarr Entegrasyonu" [pass]`,
		},
		{
			name: "karar yok (mercek hiç çalışmadı)",
			p:    store.PendingIdea{ID: 5, Title: "Kart"},
			want: `5 "Kart" [?]`,
		},
		{
			name: "başlık 60 rune'a kırpılır",
			p:    store.PendingIdea{ID: 7, Title: longTitle, DistinctivenessVerdict: sp("pass")},
			want: `7 "` + strings.Repeat("ü", 60) + `…" [pass]`,
		},
		{
			name: "veri erişimi unsure ayrı etiket olarak eklenir",
			p: store.PendingIdea{
				ID: 23, Title: "Esnaf/KOBİ için Instagram-WhatsApp Lead Takip Otomasyonu",
				DistinctivenessVerdict: sp("pass"), DistinctivenessCriterion: sp("none"),
				DataAccessVerdict: sp("unsure"),
			},
			want: `23 "Esnaf/KOBİ için Instagram-WhatsApp Lead Takip Otomasyonu" [pass] [veri erişimi: unsure]`,
		},
		{
			name: "veri erişimi NULL ise etiket hiç eklenmez",
			p: store.PendingIdea{
				ID: 24, Title: "Veri Erişimi Merceği Hiç Çalışmadı",
				DistinctivenessVerdict: sp("pass"), DistinctivenessCriterion: sp("none"),
			},
			want: `24 "Veri Erişimi Merceği Hiç Çalışmadı" [pass]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingIdeaSummary(tc.p)
			if got != tc.want {
				t.Errorf("pendingIdeaSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEliminationSummaryLine, `run` sonu eleme özeti log satırının biçimini
// doğrular (#138): sayı sıfırken (boş/nil harita) satır basılmaz (""), dolu
// harita bilinen stage'leri eliminationStageOrder sırasına göre TR
// açıklamasıyla basar, bilinmeyen stage ham adıyla (alfabetik sırada, bilinen
// stage'lerden sonra) geçer.
func TestEliminationSummaryLine(t *testing.T) {
	cases := []struct {
		name   string
		counts map[string]int
		want   string
	}{
		{
			name:   "harita nil ise satır basılmaz",
			counts: nil,
			want:   "",
		},
		{
			name:   "harita boş ise satır basılmaz",
			counts: map[string]int{},
			want:   "",
		},
		{
			name: "bilinen stage'ler sabit sırada TR açıklamayla",
			counts: map[string]int{
				"vendor_internal":  1,
				"distinctiveness":  3,
				"incoherent_theme": 2,
				"blocking_lens":    1,
			},
			want: "run: bu koşuda 7 elendi (tutarsız tema: 2, mercek: 1, doygunluk: 3, vendor: 1)",
		},
		{
			name: "bilinmeyen stage ham adıyla, bilinenlerden sonra alfabetik",
			counts: map[string]int{
				"distinctiveness": 1,
				"zzz_yeni":        2,
				"aaa_yeni":        1,
			},
			want: "run: bu koşuda 4 elendi (doygunluk: 1, aaa_yeni: 1, zzz_yeni: 2)",
		},
		{
			name:   "yalnız bilinmeyen stage'ler alfabetik sırada",
			counts: map[string]int{"zzz_yeni": 1, "aaa_yeni": 1},
			want:   "run: bu koşuda 2 elendi (aaa_yeni: 1, zzz_yeni: 1)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eliminationSummaryLine(tc.counts)
			if got != tc.want {
				t.Errorf("eliminationSummaryLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
