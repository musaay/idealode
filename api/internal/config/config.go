// Package config, IdeaLode'un tüm konfigürasyonunu ortam değişkenlerinden
// yükler. Config dosyası bilinçli olarak YOK (V1 kararı) — kaynak listesi
// DB-backed, geri kalan her şey env.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config, tek binary'nin tüm subcommand'ları için ortak konfigürasyon.
// Faz 0'da yalnızca bir kısmı kullanılır; alanlar şema gibi baştan nihai
// tutulur ki sonraki fazlarda yapı değişmesin.
type Config struct {
	// Zorunlu
	DatabaseURL string // DATABASE_URL — cv-search Railway Postgres instance'ı, idealode şeması

	// LLM (analyze/synthesize/generate için zorunlu; ingest/dump için değil)
	GroqAPIKey string // GROQ_API_KEY
	GroqModel  string // GROQ_MODEL (default: openai/gpt-oss-120b)

	// Çıktı dili — üretilen kullanıcıya dönük metinler bu dilde (Rev 2: tr).
	// EN'e geçiş = env değişikliği; tag'ler kanonik EN slug olduğu için
	// gruplama bozulmaz.
	OutputLang string // OUTPUT_LANG (default: tr)

	// Connector'lar (opsiyonel)
	StackExchangeKey string // STACKEXCHANGE_KEY — opsiyonel app key (quota artışı)
	GitHubToken      string // GITHUB_TOKEN — opsiyonel PAT (60/saat -> 5000/saat) [Faz 1]
	ProductHuntToken string // PRODUCTHUNT_TOKEN — developer token [Faz 1]

	// Reddit — wired ama uykuda: OAuth onayı açılırsa doldurulur, kod hazır. [Faz 1]
	RedditClientID     string // REDDIT_CLIENT_ID
	RedditClientSecret string // REDDIT_CLIENT_SECRET
	RedditUsername     string // REDDIT_USERNAME
	RedditPassword     string // REDDIT_PASSWORD

	// Pipeline ayarları
	MinThemeEvidence int // MIN_THEME_EVIDENCE — idea synthesis frekans eşiği (default: 3)
	LLMChunkSize     int // LLM_CHUNK_SIZE — classification batch boyutu (default: 8)
	LLMSleepMS       int // LLM_SLEEP_MS — chunk'lar arası sabit sleep, "sakin ilerleme" (default: 400)

	// Faz 2 (auth) — şimdiden tanımlı, Faz 0/1'de boş kalabilir
	AdminEmails []string // ADMIN_EMAILS — virgülle ayrık admin allowlist'i
	JWTSecret   string   // JWT_SECRET — app-JWT imza anahtarı (7 gün, refresh yok)
}

// Load, env'den Config üretir. Zorunlu değişken eksikse hangi değişkenin
// eksik olduğunu söyleyen hata döner.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		GroqAPIKey:         os.Getenv("GROQ_API_KEY"),
		GroqModel:          getenvDefault("GROQ_MODEL", "openai/gpt-oss-120b"),
		OutputLang:         getenvDefault("OUTPUT_LANG", "tr"),
		StackExchangeKey:   os.Getenv("STACKEXCHANGE_KEY"),
		GitHubToken:        os.Getenv("GITHUB_TOKEN"),
		ProductHuntToken:   os.Getenv("PRODUCTHUNT_TOKEN"),
		RedditClientID:     os.Getenv("REDDIT_CLIENT_ID"),
		RedditClientSecret: os.Getenv("REDDIT_CLIENT_SECRET"),
		RedditUsername:     os.Getenv("REDDIT_USERNAME"),
		RedditPassword:     os.Getenv("REDDIT_PASSWORD"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
	}

	var err error
	if c.MinThemeEvidence, err = getenvInt("MIN_THEME_EVIDENCE", 3); err != nil {
		return nil, err
	}
	if c.LLMChunkSize, err = getenvInt("LLM_CHUNK_SIZE", 8); err != nil {
		return nil, err
	}
	if c.LLMSleepMS, err = getenvInt("LLM_SLEEP_MS", 400); err != nil {
		return nil, err
	}

	for _, e := range strings.Split(os.Getenv("ADMIN_EMAILS"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			c.AdminEmails = append(c.AdminEmails, strings.ToLower(e))
		}
	}

	return c, nil
}

// RequireGroq, LLM gerektiren subcommand'ların başında çağrılır.
func (c *Config) RequireGroq() error {
	if c.GroqAPIKey == "" {
		return fmt.Errorf("bu komut LLM kullanır; zorunlu ortam değişkeni eksik: GROQ_API_KEY")
	}
	return nil
}

// RequireDatabaseURL, doğrudan DB'ye bağlanan subcommand'ların (ingest,
// analyze, synthesize, fuse, seeds, run, migrate, dump, api) başında
// çağrılır. `serve` DB'ye bağlanmaz (#18 — API_BASE_URL üzerinden okur),
// bu yüzden Load() DATABASE_URL'i artık zorunlu kılmaz.
func (c *Config) RequireDatabaseURL() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("bu komut veritabanına bağlanır; zorunlu ortam değişkeni eksik: DATABASE_URL")
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s geçerli bir tamsayı değil: %q", key, v)
	}
	return n, nil
}
