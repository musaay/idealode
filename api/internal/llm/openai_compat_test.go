package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatJSONRetryOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	out, err := c.ChatJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("içerik yanlış: %q", out)
	}
	if calls.Load() != 2 {
		t.Errorf("429 sonrası retry beklenirdi; çağrı sayısı: %d", calls.Load())
	}
}

func TestChatJSONNonRetryableError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "bad", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSON(context.Background(), "s", "u"); err == nil {
		t.Fatal("401'de hata beklenir")
	}
	if calls.Load() != 1 {
		t.Errorf("401 retry edilmemeli; çağrı sayısı: %d", calls.Load())
	}
}

func TestChatJSONWithTemperatureSendsZero(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSONWithTemperature(context.Background(), "sys", "user", 0); err != nil {
		t.Fatalf("ChatJSONWithTemperature: %v", err)
	}
	// omitempty KULLANILMAZ: temperature 0 iken de gövdede görünmeli (#106).
	if !strings.Contains(string(body), `"temperature":0`) {
		t.Errorf("gövdede \"temperature\":0 bekleniyordu, geldi: %s", body)
	}
}

func TestChatJSONUsesDefaultTemperature(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSON(context.Background(), "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if !strings.Contains(string(body), `"temperature":0.3`) {
		t.Errorf("gövdede \"temperature\":0.3 bekleniyordu, geldi: %s", body)
	}
}

func TestHostFallsBackToBaseURL(t *testing.T) {
	// Geçersiz/host'suz bir BaseURL verilirse hata metni yine anlamlı kalsın.
	c := &OpenAICompatClient{BaseURL: "not-a-url"}
	if got := c.host(); got != "not-a-url" {
		t.Errorf("host() ham BaseURL'e düşmeli, geldi: %q", got)
	}

	c2 := &OpenAICompatClient{BaseURL: "https://api.example.com/v1"}
	if got := c2.host(); got != "api.example.com" {
		t.Errorf("host() parse edilmiş host dönmeli, geldi: %q", got)
	}
}
