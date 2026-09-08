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
// birlikte gösterilir, karar yoksa "[?]".
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
