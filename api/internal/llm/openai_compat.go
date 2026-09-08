// Package llm, OpenAI'ın chat-completions şemasını konuşan istemci sağlar.
// Sağlayıcı (base URL/model/API key) config üzerinden env'den gelir (#96) —
// sağlayıcı değişimi kod değişikliği istemez. "Sakin ilerleme" prensibi
// (plan madde 5, cv-search pattern reuse): exponential backoff + retry-on-429,
// Retry-After header'ına uyum; sleep + retry BİRLİKTE (reprocess_cvs hatası
// tekrarlanmaz).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Chat, pipeline'ın LLM bağımlılığını soyutlar (testlerde sahte uygulama).
//
// Sıcaklık politikası (#106): yargı çağrıları (sınıflandırma, tutarlılık,
// dedup, mercekler, fuse hakemleri) ChatJSONWithTemperature ile 0 gönderir
// (aynı girdiye tutarlı karar); üretim çağrıları (kart/sohbet metni)
// ChatJSON ile 0.3'te kalır (metin çeşitliliği).
type Chat interface {
	// ChatJSON, JSON-mode'da tek tur sohbet yapar ve modelin ürettiği ham
	// JSON string'ini döner (sabit 0.3 sıcaklıkla — üretim çağrıları için).
	ChatJSON(ctx context.Context, system, user string) (string, error)

	// ChatJSONWithTemperature, ChatJSON ile aynıdır ama sıcaklığı çağıran
	// belirler (yargı çağrıları için 0).
	ChatJSONWithTemperature(ctx context.Context, system, user string, temperature float64) (string, error)
}

// OpenAICompatClient, OpenAI'ın chat-completions şemasıyla uyumlu herhangi
// bir endpoint'i kullanır (base URL config'ten gelir, test için override
// edilebilir).
type OpenAICompatClient struct {
	APIKey     string
	Model      string
	BaseURL    string // test için override edilebilir
	HTTPClient *http.Client
}

// NewOpenAICompat canlı istemci döner.
func NewOpenAICompat(baseURL, apiKey, model string) *OpenAICompatClient {
	return &OpenAICompatClient{
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
	}
}

const maxRetries = 3

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type respFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// defaultTemperature, üretim çağrıları (kart/sohbet metni) için sabit
// sıcaklıktır — metin çeşitliliği istenir (#106).
const defaultTemperature = 0.3

func (c *OpenAICompatClient) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, defaultTemperature)
}

func (c *OpenAICompatClient) ChatJSONWithTemperature(ctx context.Context, system, user string, temperature float64) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature:    temperature,
		ResponseFormat: &respFormat{Type: "json_object"},
	})
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelay(lastErr, attempt)):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		content, retryable, err := c.doRequest(ctx, payload)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("%s: %d denemede başarısız: %w", c.host(), maxRetries+1, lastErr)
}

// rateLimitError, Retry-After bilgisini backoff hesabına taşır.
type rateLimitError struct {
	host       string // hata metninde sağlayıcı adı yerine base URL host'u kullanılır
	status     int
	retryAfter time.Duration
	body       string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("%s HTTP %d: %s", e.host, e.status, e.body)
}

func retryDelay(err error, attempt int) time.Duration {
	if rle, ok := err.(*rateLimitError); ok && rle.retryAfter > 0 {
		return rle.retryAfter
	}
	return time.Duration(1<<attempt) * time.Second // 2s, 4s, 8s
}

func (c *OpenAICompatClient) doRequest(ctx context.Context, payload []byte) (content string, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", true, err // ağ hatası — denemeye değer
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", true, err
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		var after time.Duration
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, perr := strconv.ParseFloat(s, 64); perr == nil {
				after = time.Duration(secs * float64(time.Second))
			}
		}
		return "", true, &rateLimitError{host: c.host(), status: resp.StatusCode, retryAfter: after, body: truncate(string(body), 200)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("%s HTTP %d: %s", c.host(), resp.StatusCode, truncate(string(body), 400))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, fmt.Errorf("%s yanıtı parse edilemedi: %w", c.host(), err)
	}
	if parsed.Error != nil {
		return "", false, fmt.Errorf("%s: %s", c.host(), parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("%s: boş yanıt", c.host())
	}
	return parsed.Choices[0].Message.Content, false, nil
}

// host, hata metinlerinde sağlayıcı adı yerine kullanılan base URL host'unu
// döner (#96 — sağlayıcı artık koda çakılı değil).
func (c *OpenAICompatClient) host() string {
	if u, err := url.Parse(c.BaseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.BaseURL
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
