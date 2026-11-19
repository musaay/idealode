package config

import "testing"

// TestMomentumCardPromptEnv (#221): "v4" kabul; geçersiz/boş -> "v1".
func TestMomentumCardPromptEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for env, want := range map[string]string{"v4": "v4", "v1": "v1", "": "v1", "v3": "v1", "xyz": "v1"} {
		t.Setenv("MOMENTUM_CARD_PROMPT", env)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.MomentumCardPrompt != want {
			t.Errorf("MOMENTUM_CARD_PROMPT=%q: %q beklenirdi, geldi %q", env, want, c.MomentumCardPrompt)
		}
	}
}
