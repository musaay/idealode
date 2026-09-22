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

// LLM sağlayıcısı için varsayılanlar (#96). Sağlayıcı env ile seçilir; kod
// değişikliği gerekmez. Varsayılan değerler geriye dönük olarak Groq'u
// hedefler.
const (
	defaultLLMBaseURL = "https://api.groq.com/openai/v1"
	defaultLLMModel   = "openai/gpt-oss-120b"
)

// Config, tek binary'nin tüm subcommand'ları için ortak konfigürasyon.
// Faz 0'da yalnızca bir kısmı kullanılır; alanlar şema gibi baştan nihai
// tutulur ki sonraki fazlarda yapı değişmesin.
type Config struct {
	// Zorunlu
	DatabaseURL string // DATABASE_URL — cv-search Railway Postgres instance'ı, idealode şeması

	// LLM (analyze/synthesize/generate için zorunlu; ingest/dump için değil).
	// Sağlayıcı env ile seçilir (#96): LLM_BASE_URL/LLM_MODEL/LLM_API_KEY
	// boşsa GROQ_API_KEY/GROQ_MODEL'e düşer (Railway'de mevcut env bozulmasın).
	LLMBaseURL string // LLM_BASE_URL (default: https://api.groq.com/openai/v1; sondaki "/" kırpılır)
	LLMModel   string // LLM_MODEL (default: openai/gpt-oss-120b); boşsa GROQ_MODEL
	LLMAPIKey  string // LLM_API_KEY; boşsa GROQ_API_KEY

	// Özgünlük merceği için AYRI istemci (#166 — v4 canlıda K1'i tutarsız
	// uyguladığı ölçüldü, Gemini 3.5 Flash Lite'a taşındı; yalnız bu mercek).
	// Üçü de (BASE_URL/API_KEY/MODEL) doluysa evaluateDistinctiveness bu
	// istemciyi kullanır — bkz. UseDistinctivenessLLM(); biri eksikse
	// özgünlük de varsayılan LLM istemcisini kullanır (bugünkü davranış
	// birebir, geriye uyumlu).
	DistinctivenessLLMBaseURL string // DISTINCTIVENESS_LLM_BASE_URL (sondaki "/" kırpılır)
	DistinctivenessLLMModel   string // DISTINCTIVENESS_LLM_MODEL
	DistinctivenessLLMAPIKey  string // DISTINCTIVENESS_LLM_API_KEY

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

	// PreferPaymentSignal, sentez sıralama sinyali (#125): açıkken
	// ThemesReadyForSynthesis ödeme sinyali taşıyan (en az bir postu
	// willingness_to_pay=true) temaları öne alır — ELEMEZ, yalnız sıralar.
	// #121'de sert eleme olarak eklenmişti; ödeme sinyali nadir olduğu için
	// (%0,3) üretimde kart akışını sıfıra düşürdü, bu yüzden #125 ile
	// sıralamaya çevrildi. Tohum/radar (market_derived) yolunu etkilemez.
	PreferPaymentSignal bool // PREFER_PAYMENT_SIGNAL (default: true); eski REQUIRE_PAYMENT_SIGNAL hâlâ okunur (geriye dönük uyum)

	// Faz 2 (auth) — şimdiden tanımlı, Faz 0/1'de boş kalabilir
	AdminEmails []string // ADMIN_EMAILS — virgülle ayrık admin allowlist'i
	JWTSecret   string   // JWT_SECRET — app-JWT imza anahtarı (7 gün, refresh yok)
}

// Load, env'den Config üretir. Zorunlu değişken eksikse hangi değişkenin
// eksik olduğunu söyleyen hata döner.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
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
	c.LLMBaseURL, c.LLMModel, c.LLMAPIKey = loadLLMEnv()
	c.DistinctivenessLLMBaseURL = strings.TrimSuffix(os.Getenv("DISTINCTIVENESS_LLM_BASE_URL"), "/")
	c.DistinctivenessLLMModel = os.Getenv("DISTINCTIVENESS_LLM_MODEL")
	c.DistinctivenessLLMAPIKey = os.Getenv("DISTINCTIVENESS_LLM_API_KEY")

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
	if c.PreferPaymentSignal, err = loadPreferPaymentSignal(); err != nil {
		return nil, err
	}

	for _, e := range strings.Split(os.Getenv("ADMIN_EMAILS"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			c.AdminEmails = append(c.AdminEmails, strings.ToLower(e))
		}
	}

	return c, nil
}

// loadLLMEnv, LLM_* değişkenlerini okur; her biri boşsa sırayla eski
// GROQ_* değişkenine, o da yoksa sabit varsayılana düşer (#96 — geriye
// uyumluluk, Railway'deki mevcut env bozulmasın). Base URL sonundaki "/"
// kırpılır ki `BaseURL+"/chat/completions"` birleştirmesi çift eğik çizgi
// üretmesin.
func loadLLMEnv() (baseURL, model, apiKey string) {
	apiKey = os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GROQ_API_KEY")
	}

	model = os.Getenv("LLM_MODEL")
	if model == "" {
		model = getenvDefault("GROQ_MODEL", defaultLLMModel)
	}

	baseURL = strings.TrimSuffix(getenvDefault("LLM_BASE_URL", defaultLLMBaseURL), "/")
	return
}

// loadPreferPaymentSignal, PREFER_PAYMENT_SIGNAL'i okur; tanımlı değilse
// eski REQUIRE_PAYMENT_SIGNAL'a düşer (#125 — kapı #121'de sert eleme
// olarak eklenmişti, adı artık yanıltıcı olduğu için değişti; Railway'deki
// eski env adıyla çalışan üretim servisi bozulmasın). İkisi de tanımsızsa
// varsayılan true.
func loadPreferPaymentSignal() (bool, error) {
	if os.Getenv("PREFER_PAYMENT_SIGNAL") != "" {
		return getenvBool("PREFER_PAYMENT_SIGNAL", true)
	}
	return getenvBool("REQUIRE_PAYMENT_SIGNAL", true)
}

// UseDistinctivenessLLM, özgünlük merceği için AYRI bir llm.Chat istemcisi
// kurulup kurulmayacağını bildirir (#166): DISTINCTIVENESS_LLM_BASE_URL/
// API_KEY/MODEL üçü de doluysa true. Biri eksikse false — çağıran (cmd/
// idealode) özgünlük için de varsayılan istemciyi kullanır, kısmi/yanlış
// yapılandırma sessizce yarım çalışmaz.
func (c *Config) UseDistinctivenessLLM() bool {
	return c.DistinctivenessLLMBaseURL != "" && c.DistinctivenessLLMAPIKey != "" && c.DistinctivenessLLMModel != ""
}

// RequireLLM, LLM gerektiren subcommand'ların başında çağrılır.
func (c *Config) RequireLLM() error {
	if c.LLMAPIKey == "" {
		return fmt.Errorf("bu komut LLM kullanır; zorunlu ortam değişkeni eksik: LLM_API_KEY (veya GROQ_API_KEY)")
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

func getenvBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s geçerli bir bool değil (true/false): %q", key, v)
	}
	return b, nil
}
