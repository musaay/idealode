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
	if !c.RequirePaymentSignal {
		t.Error("RequirePaymentSignal default true olmalı (#121)")
	}
}

// TestRequirePaymentSignalEnv, #121 kapısının env ile kapatılabildiğini ve
// geçersiz değerde net hata döndüğünü doğrular.
func TestRequirePaymentSignalEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Run("REQUIRE_PAYMENT_SIGNAL=false -> kapı kapalı", func(t *testing.T) {
		t.Setenv("REQUIRE_PAYMENT_SIGNAL", "false")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.RequirePaymentSignal {
			t.Error("RequirePaymentSignal false olmalı")
		}
	})

	t.Run("geçersiz değer -> hata", func(t *testing.T) {
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
