package config

import "testing"

// TestCompetitorCheckEnv (#212 adım 3): varsayılan off; "on" kabul; çöp -> off.
func TestCompetitorCheckEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for env, want := range map[string]bool{"": false, "off": false, "on": true, " on ": true, "true": false, "ON": false} {
		t.Setenv("COMPETITOR_CHECK", env)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.CompetitorCheck != want {
			t.Errorf("COMPETITOR_CHECK=%q: %v beklenirdi, geldi %v", env, want, c.CompetitorCheck)
		}
	}
	t.Setenv("COMPETITOR_CHECK_MODEL", "")
	c, _ := Load()
	if c.CompetitorModel != "openai/gpt-oss-120b" {
		t.Errorf("varsayılan model yanlış: %q", c.CompetitorModel)
	}
	t.Setenv("COMPETITOR_CHECK_MODEL", "openai/gpt-oss-20b")
	c, _ = Load()
	if c.CompetitorModel != "openai/gpt-oss-20b" {
		t.Errorf("model env okunmadı: %q", c.CompetitorModel)
	}
}
