package main

import (
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
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

// TestFormatThousands, binlik ayraçlı (nokta) sayı biçimlendirmeyi doğrular
// (#144) — token log satırındaki tüm sayılar bu yardımcıdan geçer.
func TestFormatThousands(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{9, "9"},
		{999, "999"},
		{1000, "1.000"},
		{142310, "142.310"},
		{98400, "98.400"},
		{1234567, "1.234.567"},
		{-2500, "-2.500"},
	}
	for _, tc := range cases {
		if got := formatThousands(tc.n); got != tc.want {
			t.Errorf("formatThousands(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestUsageSummaryLine, `run: token kullanımı …` özet satırının biçimini
// doğrular (#144): token'a göre büyükten küçüğe sıralanır (eşitlikte aşama
// adı alfabetik), binlik ayraç nokta, toplam 0 ise satır boş döner.
func TestUsageSummaryLine(t *testing.T) {
	cases := []struct {
		name string
		snap map[string]llm.StageUsage
		want string
	}{
		{
			name: "harita nil ise satır basılmaz",
			snap: nil,
			want: "",
		},
		{
			name: "toplam token 0 ise satır basılmaz (çağrı olsa bile)",
			snap: map[string]llm.StageUsage{
				"analiz": {Calls: 3, TotalTokens: 0},
			},
			want: "",
		},
		{
			name: "aşamalar token'a göre büyükten küçüğe sıralanır",
			snap: map[string]llm.StageUsage{
				"analiz":     {Calls: 61, TotalTokens: 98400},
				"kümeleme":   {Calls: 12, TotalTokens: 21050},
				"tutarlılık": {Calls: 9, TotalTokens: 14200},
			},
			want: "run: token kullanımı — toplam 133.650 · analiz 98.400 (61 çağrı) · kümeleme 21.050 (12 çağrı) · tutarlılık 14.200 (9 çağrı)",
		},
		{
			name: "eşit token'da aşama adı alfabetik sıralanır",
			snap: map[string]llm.StageUsage{
				"zzz": {Calls: 1, TotalTokens: 100},
				"aaa": {Calls: 1, TotalTokens: 100},
			},
			want: "run: token kullanımı — toplam 200 · aaa 100 (1 çağrı) · zzz 100 (1 çağrı)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usageSummaryLine(tc.snap)
			if got != tc.want {
				t.Errorf("usageSummaryLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
