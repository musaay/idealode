package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("OUTPUT_LANG", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("GROQ_MODEL", "")
	t.Setenv("LLM_BASE_URL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.OutputLang != "tr" {
		t.Errorf("OutputLang default tr olmalı, geldi: %q", c.OutputLang)
	}
	if c.LLMModel != "openai/gpt-oss-120b" {
		t.Errorf("LLMModel default'u yanlış: %q", c.LLMModel)
	}
	if c.LLMBaseURL != defaultLLMBaseURL {
		t.Errorf("LLMBaseURL default'u yanlış: %q", c.LLMBaseURL)
	}
	if c.MinThemeEvidence != 3 || c.LLMChunkSize != 8 || c.LLMSleepMS != 400 {
		t.Errorf("sayısal default'lar yanlış: %+v", c)
	}
	if !c.PreferPaymentSignal {
		t.Error("PreferPaymentSignal default true olmalı (#125)")
	}
	if c.BlendEnabled {
		t.Error("BlendEnabled default false olmalı (#186)")
	}
}

// TestPreferPaymentSignalEnv, #125 sıralama sinyalinin env ile
// kapatılabildiğini, eski REQUIRE_PAYMENT_SIGNAL adının geriye dönük
// çalıştığını, yeni PREFER_PAYMENT_SIGNAL tanımlıyken onu geçersiz
// kıldığını ve geçersiz değerde net hata döndüğünü doğrular.
func TestPreferPaymentSignalEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Run("PREFER_PAYMENT_SIGNAL=false -> sıralama sinyali kapalı", func(t *testing.T) {
		t.Setenv("PREFER_PAYMENT_SIGNAL", "false")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.PreferPaymentSignal {
			t.Error("PreferPaymentSignal false olmalı")
		}
	})

	t.Run("yalnız eski REQUIRE_PAYMENT_SIGNAL=false -> geriye dönük uyum", func(t *testing.T) {
		t.Setenv("PREFER_PAYMENT_SIGNAL", "")
		t.Setenv("REQUIRE_PAYMENT_SIGNAL", "false")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.PreferPaymentSignal {
			t.Error("eski REQUIRE_PAYMENT_SIGNAL=false hâlâ okunmalı, PreferPaymentSignal false olmalı")
		}
	})

	t.Run("ikisi de tanımlı -> PREFER_PAYMENT_SIGNAL kazanır", func(t *testing.T) {
		t.Setenv("PREFER_PAYMENT_SIGNAL", "true")
		t.Setenv("REQUIRE_PAYMENT_SIGNAL", "false")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !c.PreferPaymentSignal {
			t.Error("PREFER_PAYMENT_SIGNAL tanımlıyken önceliği o almalı")
		}
	})

	t.Run("geçersiz PREFER_PAYMENT_SIGNAL -> hata", func(t *testing.T) {
		t.Setenv("PREFER_PAYMENT_SIGNAL", "evet")
		t.Setenv("REQUIRE_PAYMENT_SIGNAL", "")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PREFER_PAYMENT_SIGNAL") {
			t.Errorf("geçersiz PREFER_PAYMENT_SIGNAL adıyla raporlanmalı, geldi: %v", err)
		}
	})

	t.Run("geçersiz eski REQUIRE_PAYMENT_SIGNAL -> hata", func(t *testing.T) {
		t.Setenv("PREFER_PAYMENT_SIGNAL", "")
		t.Setenv("REQUIRE_PAYMENT_SIGNAL", "evet")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REQUIRE_PAYMENT_SIGNAL") {
			t.Errorf("geçersiz REQUIRE_PAYMENT_SIGNAL adıyla raporlanmalı, geldi: %v", err)
		}
	})
}

func TestLoadWithoutDatabaseURL(t *testing.T) {
	// serve süreci DB'ye bağlanmaz (#18) — DATABASE_URL yokken Load() geçmeli.
	t.Setenv("DATABASE_URL", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("DATABASE_URL olmadan Load hata vermemeli: %v", err)
	}
	if c.DatabaseURL != "" {
		t.Errorf("DatabaseURL boş kalmalı, geldi: %q", c.DatabaseURL)
	}
}

func TestRequireDatabaseURL(t *testing.T) {
	c := &Config{}
	if err := c.RequireDatabaseURL(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("DATABASE_URL eksikliği adıyla raporlanmalı, geldi: %v", err)
	}
	c.DatabaseURL = "postgres://x"
	if err := c.RequireDatabaseURL(); err != nil {
		t.Errorf("DatabaseURL varken hata olmamalı: %v", err)
	}
}

func TestAdminEmailsParsing(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("ADMIN_EMAILS", " A@b.com, ,c@d.com ")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.AdminEmails) != 2 || c.AdminEmails[0] != "a@b.com" || c.AdminEmails[1] != "c@d.com" {
		t.Errorf("AdminEmails parse hatası: %v", c.AdminEmails)
	}
}

// TestUseDistinctivenessLLM, özgünlük merceği için AYRI istemci kurulup
// kurulmayacağını bildiren UseDistinctivenessLLM'in yalnız üçü de
// (BASE_URL/API_KEY/MODEL) doluyken true döndüğünü doğrular (#166).
func TestUseDistinctivenessLLM(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Run("üçü de boş -> false", func(t *testing.T) {
		t.Setenv("DISTINCTIVENESS_LLM_BASE_URL", "")
		t.Setenv("DISTINCTIVENESS_LLM_API_KEY", "")
		t.Setenv("DISTINCTIVENESS_LLM_MODEL", "")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.UseDistinctivenessLLM() {
			t.Error("üçü de boşken UseDistinctivenessLLM false olmalı")
		}
	})

	t.Run("biri eksik -> false", func(t *testing.T) {
		t.Setenv("DISTINCTIVENESS_LLM_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai")
		t.Setenv("DISTINCTIVENESS_LLM_API_KEY", "gemini-key")
		t.Setenv("DISTINCTIVENESS_LLM_MODEL", "")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.UseDistinctivenessLLM() {
			t.Error("MODEL eksikken UseDistinctivenessLLM false olmalı")
		}
	})

	t.Run("üçü de dolu -> true, sondaki / kırpılır", func(t *testing.T) {
		t.Setenv("DISTINCTIVENESS_LLM_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai/")
		t.Setenv("DISTINCTIVENESS_LLM_API_KEY", "gemini-key")
		t.Setenv("DISTINCTIVENESS_LLM_MODEL", "gemini-3.5-flash-lite")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !c.UseDistinctivenessLLM() {
			t.Error("üçü de doluyken UseDistinctivenessLLM true olmalı")
		}
		if c.DistinctivenessLLMBaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
			t.Errorf("DistinctivenessLLMBaseURL sondaki / kırpılmalı, geldi: %q", c.DistinctivenessLLMBaseURL)
		}
		if c.DistinctivenessLLMModel != "gemini-3.5-flash-lite" {
			t.Errorf("DistinctivenessLLMModel yanlış: %q", c.DistinctivenessLLMModel)
		}
		if c.DistinctivenessLLMAPIKey != "gemini-key" {
			t.Errorf("DistinctivenessLLMAPIKey yanlış: %q", c.DistinctivenessLLMAPIKey)
		}
	})
}

// TestDistinctivenessVotesEnv, DISTINCTIVENESS_VOTES ayrıştırmasını
// doğrular (#181): boş->1, geçerli sayı->kendisi, geçersiz/<1->1, üst
// sınırın (5) üstü->5.
func TestDistinctivenessVotesEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	cases := []struct {
		env  string
		want int
	}{
		{"", 1},
		{"3", 3},
		{"0", 1},
		{"x", 1},
		{"9", 5},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("DISTINCTIVENESS_VOTES", tc.env)
			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.DistinctivenessVotes != tc.want {
				t.Errorf("DISTINCTIVENESS_VOTES=%q: %d beklenirdi, geldi %d", tc.env, tc.want, c.DistinctivenessVotes)
			}
		})
	}
}

// TestBlendEnabledEnv, BLEND_ENABLED ayrıştırmasını doğrular (#186): boş->
// false, "true"/"1"/" TRUE " (boşluklu, büyük harf)->true, "0"/"x" gibi
// başka her değer->false.
func TestBlendEnabledEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"true", true},
		{"1", true},
		{" TRUE ", true},
		{"0", false},
		{"x", false},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("BLEND_ENABLED", tc.env)
			c, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.BlendEnabled != tc.want {
				t.Errorf("BLEND_ENABLED=%q: %v beklenirdi, geldi %v", tc.env, tc.want, c.BlendEnabled)
			}
		})
	}
}

func TestRequireLLM(t *testing.T) {
	c := &Config{}
	if err := c.RequireLLM(); err == nil || !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Errorf("LLM_API_KEY eksikliği adıyla raporlanmalı, geldi: %v", err)
	}
	c.LLMAPIKey = "gsk_x"
	if err := c.RequireLLM(); err != nil {
		t.Errorf("key varken hata olmamalı: %v", err)
	}
}

// TestLLMEnvFallback, #96 geriye uyumluluk sözleşmesini doğrular: LLM_*
// boşsa GROQ_*'a düşülür, ikisi de doluysa LLM_* kazanır, ikisi de boşsa
// RequireLLM net hata verir.
func TestLLMEnvFallback(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Run("LLM_ boş + GROQ_ dolu -> GROQ değerleri kullanılır", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "")
		t.Setenv("LLM_MODEL", "")
		t.Setenv("LLM_BASE_URL", "")
		t.Setenv("GROQ_API_KEY", "gsk_old")
		t.Setenv("GROQ_MODEL", "old-model")

		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.LLMAPIKey != "gsk_old" {
			t.Errorf("LLMAPIKey GROQ_API_KEY'e düşmeli, geldi: %q", c.LLMAPIKey)
		}
		if c.LLMModel != "old-model" {
			t.Errorf("LLMModel GROQ_MODEL'e düşmeli, geldi: %q", c.LLMModel)
		}
		if c.LLMBaseURL != defaultLLMBaseURL {
			t.Errorf("LLMBaseURL default kalmalı, geldi: %q", c.LLMBaseURL)
		}
	})

	t.Run("ikisi de dolu -> LLM_ kazanır, sondaki / kırpılır", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "llm_new")
		t.Setenv("LLM_MODEL", "new-model")
		t.Setenv("LLM_BASE_URL", "https://example.com/v1/")
		t.Setenv("GROQ_API_KEY", "gsk_old")
		t.Setenv("GROQ_MODEL", "old-model")

		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.LLMAPIKey != "llm_new" {
			t.Errorf("LLMAPIKey LLM_API_KEY kazanmalı, geldi: %q", c.LLMAPIKey)
		}
		if c.LLMModel != "new-model" {
			t.Errorf("LLMModel LLM_MODEL kazanmalı, geldi: %q", c.LLMModel)
		}
		if c.LLMBaseURL != "https://example.com/v1" {
			t.Errorf("LLMBaseURL sondaki / kırpılmalı, geldi: %q", c.LLMBaseURL)
		}
	})

	t.Run("ikisi de boş -> RequireLLM hata verir", func(t *testing.T) {
		t.Setenv("LLM_API_KEY", "")
		t.Setenv("GROQ_API_KEY", "")

		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := c.RequireLLM(); err == nil {
			t.Error("LLM_API_KEY ve GROQ_API_KEY ikisi de boşken RequireLLM hata vermeli")
		}
	})
}
