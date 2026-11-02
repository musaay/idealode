package config

import "testing"

// COMPETITOR_SEARCHES: varsayılan 3; 1..5 kabul; geçersiz -> 3.
func TestCompetitorSearchesEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for env, want := range map[string]int{"": 3, "1": 1, "5": 5, " 4 ": 4, "0": 3, "6": 3, "-2": 3, "abc": 3} {
		t.Setenv("COMPETITOR_SEARCHES", env)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.CompetitorSearches != want {
			t.Errorf("COMPETITOR_SEARCHES=%q: %d beklenirdi, geldi %d", env, want, c.CompetitorSearches)
		}
	}
}
