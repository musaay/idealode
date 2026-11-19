package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/config"
)

// --- #221: ivme kartı istemi v4 (MOMENTUM_CARD_PROMPT) ---

// TestMomentumCardPromptsV1Unchanged: "" ve "v1" -> ivme dalı bugünkü v1 ile
// byte-byte aynı (SEED_CARD_PROMPT hangi değerde olursa olsun).
func TestMomentumCardPromptsV1Unchanged(t *testing.T) {
	seed := radarSeed{Name: "N", Summary: "S", Evidence: "E", SourceURL: "https://x.example", TRAngle: "TR açısı"}
	gate := trendingGateResult{repo: "o/r", stars: 100, lastDelta: 5, persistedDays: 3}
	for _, m := range []string{"", "v1"} {
		for _, sc := range []string{"", "v1", "v3", "v4"} {
			cfg := &config.Config{OutputLang: "tr", MomentumCardPrompt: m, SeedCardPrompt: sc}
			sys, usr := seedCardPrompts(cfg, seed, true, gate)
			if sys != fmt.Sprintf(seedMomentumCardSystemTmpl, langName("tr")) ||
				usr != seedMomentumCardUserPrompt(seed, "o/r", 100, 5, 3) {
				t.Errorf("momentum=%q seedCard=%q: ivme istemi v1'den farklı", m, sc)
			}
		}
	}
}

// TestMomentumCardPromptsV4: V4 sistem + açısız kullanıcı istemi.
func TestMomentumCardPromptsV4(t *testing.T) {
	seed := radarSeed{Name: "N", Summary: "S", Evidence: "E", SourceURL: "https://x.example", TRAngle: "TR açısı"}
	gate := trendingGateResult{repo: "o/r", stars: 100, lastDelta: 5, persistedDays: 3}
	cfg := &config.Config{OutputLang: "tr", MomentumCardPrompt: "v4"}
	sys, usr := seedCardPrompts(cfg, seed, true, gate)
	if sys != fmt.Sprintf(seedMomentumCardSystemTmplV4, langName("tr")) {
		t.Error("v4 ivme sistem istemi V4 şablonu olmalı")
	}
	if strings.Contains(usr, "TR angle") || strings.Contains(usr, seed.TRAngle) {
		t.Errorf("v4 kullanıcı istemi açı taşımamalı: %q", usr)
	}
	if !strings.Contains(usr, "Growth evidence:") || !strings.HasSuffix(usr, "Source: https://x.example") {
		t.Errorf("v4 kullanıcı istemi büyüme kanıtı + kaynakla bitmeli: %q", usr)
	}
	if n := strings.Count(seedMomentumCardSystemTmplV4, "%"); n != 1 || strings.Count(seedMomentumCardSystemTmplV4, "%s") != 1 {
		t.Errorf("V4 şablonunda tam bir %%s olmalı (%% sayısı %d)", n)
	}
}

// TestMomentumCardPromptDoesNotAffectRevenue: gelir tohumu MomentumCardPrompt'tan etkilenmez.
func TestMomentumCardPromptDoesNotAffectRevenue(t *testing.T) {
	seed := radarSeed{Name: "N", Summary: "S", Evidence: "E", SourceURL: "https://x.example", TRAngle: "TR açısı"}
	gate := trendingGateResult{}
	base := &config.Config{OutputLang: "tr"}
	v4 := &config.Config{OutputLang: "tr", MomentumCardPrompt: "v4"}
	s1, u1 := seedCardPrompts(base, seed, false, gate)
	s2, u2 := seedCardPrompts(v4, seed, false, gate)
	if s1 != s2 || u1 != u2 {
		t.Error("gelir tohumu istemi MomentumCardPrompt'tan etkilenmemeli")
	}
}
