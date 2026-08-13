// Package llm, Groq chat-completions istemcisi. "Sakin ilerleme" prensibi
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
	"strconv"
	"time"
)

// Chat, pipeline'ın LLM bağımlılığını soyutlar (testlerde sahte uygulama).
type Chat interface {
	// ChatJSON, JSON-mode'da tek tur sohbet yapar ve modelin ürettiği ham
	// JSON string'ini döner.
	ChatJSON(ctx context.Context, system, user string) (string, error)
}

// GroqClient, api.groq.com'un OpenAI-uyumlu endpoint'ini kullanır.
type GroqClient struct {
	APIKey     string
	Model      string
	BaseURL    string // test için override edilebilir
	HTTPClient *http.Client
}

// NewGroq canlı Groq API istemcisi döner.
func NewGroq(apiKey, model string) *GroqClient {
	return &GroqClient{
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    "https://api.groq.com/openai/v1",
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

func (c *GroqClient) ChatJSON(ctx context.Context, system, user string) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature:    0.3,
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
	return "", fmt.Errorf("groq: %d denemede başarısız: %w", maxRetries+1, lastErr)
}

// rateLimitError, Retry-After bilgisini backoff hesabına taşır.
type rateLimitError struct {
	status     int
	retryAfter time.Duration
	body       string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("groq HTTP %d: %s", e.status, e.body)
}

func retryDelay(err error, attempt int) time.Duration {
	if rle, ok := err.(*rateLimitError); ok && rle.retryAfter > 0 {
		return rle.retryAfter
	}
	return time.Duration(1<<attempt) * time.Second // 2s, 4s, 8s
}

func (c *GroqClient) doRequest(ctx context.Context, payload []byte) (content string, retryable bool, err error) {
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
		return "", true, &rateLimitError{status: resp.StatusCode, retryAfter: after, body: truncate(string(body), 200)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("groq HTTP %d: %s", resp.StatusCode, truncate(string(body), 400))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, fmt.Errorf("groq yanıtı parse edilemedi: %w", err)
	}
	if parsed.Error != nil {
		return "", false, fmt.Errorf("groq: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("groq: boş yanıt")
	}
	return parsed.Choices[0].Message.Content, false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
