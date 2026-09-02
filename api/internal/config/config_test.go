package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("OUTPUT_LANG", "")
	t.Setenv("GROQ_MODEL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.OutputLang != "tr" {
		t.Errorf("OutputLang default tr olmalı, geldi: %q", c.OutputLang)
	}
	if c.GroqModel != "openai/gpt-oss-120b" {
		t.Errorf("GroqModel default'u yanlış: %q", c.GroqModel)
	}
	if c.MinThemeEvidence != 3 || c.LLMChunkSize != 8 || c.LLMSleepMS != 400 {
		t.Errorf("sayısal default'lar yanlış: %+v", c)
	}
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

func TestRequireGroq(t *testing.T) {
	c := &Config{}
	if err := c.RequireGroq(); err == nil || !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Errorf("GROQ_API_KEY eksikliği adıyla raporlanmalı, geldi: %v", err)
	}
	c.GroqAPIKey = "gsk_x"
	if err := c.RequireGroq(); err != nil {
		t.Errorf("key varken hata olmamalı: %v", err)
	}
}
