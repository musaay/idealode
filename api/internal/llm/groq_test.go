package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	c := &GroqClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
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

	c := &GroqClient{APIKey: "bad", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSON(context.Background(), "s", "u"); err == nil {
		t.Fatal("401'de hata beklenir")
	}
	if calls.Load() != 1 {
		t.Errorf("401 retry edilmemeli; çağrı sayısı: %d", calls.Load())
	}
}
