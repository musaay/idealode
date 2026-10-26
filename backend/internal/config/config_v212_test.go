package config

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// TestSeedCardPromptEnvV4 (#212): SEED_CARD_PROMPT "v4"ü kabul eder; v2/v5 reddedilir.
func TestSeedCardPromptEnvV4(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for env, want := range map[string]string{"v4": "v4", " v4 ": "v4", "v5": "v1", "v6": "v1", "": "v1"} {
		t.Setenv("SEED_CARD_PROMPT", env)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.SeedCardPrompt != want {
			t.Errorf("SEED_CARD_PROMPT=%q: %q beklenirdi, geldi %q", env, want, c.SeedCardPrompt)
		}
	}
}

// TestDistinctivenessPromptEnv (#212): boş/v4 -> v4, v6 -> v6, başka (v5 dahil)
// -> v4 + log uyarısı.
func TestDistinctivenessPromptEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	cases := []struct {
		env      string
		want     string
		wantWarn bool
	}{
		{"", "v4", false},
		{"v4", "v4", false},
		{"v6", "v6", false},
		{" v6 ", "v6", false},
		{"v5", "v4", true},
		{"v1", "v4", true},
		{"V6", "v4", true},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("DISTINCTIVENESS_PROMPT", tc.env)
			var buf bytes.Buffer
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.DistinctivenessPrompt != tc.want {
				t.Errorf("DISTINCTIVENESS_PROMPT=%q: %q beklenirdi, geldi %q", tc.env, tc.want, c.DistinctivenessPrompt)
			}
			if warned := strings.Contains(buf.String(), "DISTINCTIVENESS_PROMPT geçersiz"); warned != tc.wantWarn {
				t.Errorf("uyarı beklentisi %v, geldi %v", tc.wantWarn, warned)
			}
		})
	}
}

// TestOtherPromptEnvsStillRejectV4V6 (kabul 3): THIRD_PARTY_PROMPT ve
// DATA_ACCESS_PROMPT "v4"/"v6"yı HÂLÂ reddeder (v1'e düşer + uyarı).
func TestOtherPromptEnvsStillRejectV4V6(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for _, env := range []string{"THIRD_PARTY_PROMPT", "DATA_ACCESS_PROMPT"} {
		for _, v := range []string{"v4", "v6"} {
			t.Run(env+"="+v, func(t *testing.T) {
				t.Setenv(env, v)
				var buf bytes.Buffer
				log.SetOutput(&buf)
				t.Cleanup(func() { log.SetOutput(os.Stderr) })

				c, err := Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				got := c.ThirdPartyPrompt
				if env == "DATA_ACCESS_PROMPT" {
					got = c.DataAccessPrompt
				}
				if got != "v1" {
					t.Errorf("%s=%s: v1 beklenirdi, geldi %q", env, v, got)
				}
				if !strings.Contains(buf.String(), env+" geçersiz") {
					t.Errorf("%s=%s: uyarı logu beklenirdi", env, v)
				}
			})
		}
	}
}
