package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureServer(t *testing.T, content string, got *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		})
	}))
}

// Mevcut çağrılarda payload byte-byte aynı kalır (tools/reasoning_effort girmez).
func TestChatRequestPayloadUnchanged(t *testing.T) {
	var got []byte
	srv := captureServer(t, "{}", &got)
	defer srv.Close()
	c := &OpenAICompatClient{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSONWithTemperature(context.Background(), "sys", "usr", 0); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"usr"}],"temperature":0,"response_format":{"type":"json_object"}}`
	if string(got) != want {
		t.Errorf("payload değişti:\n got %s\nwant %s", got, want)
	}
}

func TestChatWithBrowserSearchPayloadAndCitations(t *testing.T) {
	var got []byte
	srv := captureServer(t, `{"a":"x【1†source】y【12†L3-L5】"}`, &got)
	defer srv.Close()
	c := &OpenAICompatClient{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	var sc SearchChat = c
	out, err := sc.ChatWithBrowserSearch(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"a":"xy"}` {
		t.Errorf("atıf işaretleri silinmedi: %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["response_format"]; has {
		t.Error("response_format GÖNDERİLMEMELİ")
	}
	if m["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort=low beklenirdi: %v", m["reasoning_effort"])
	}
	tools, _ := m["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "browser_search" {
		t.Errorf("tools=[browser_search] beklenirdi: %v", m["tools"])
	}
	if m["temperature"] != float64(0) {
		t.Errorf("temperature 0 beklenirdi: %v", m["temperature"])
	}
}
