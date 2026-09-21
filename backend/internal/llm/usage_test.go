package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestUsageParsedAndRecorded: başarılı cevaptaki usage alanı ctx'teki
// meter'a, ctx'teki aşama etiketiyle eklenir (#144).
func TestUsageParsedAndRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	meter := NewUsageMeter()
	ctx := WithStage(WithMeter(context.Background(), meter), "analiz")
	if _, err := c.ChatJSON(ctx, "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}

	snap := meter.Snapshot()
	got, ok := snap["analiz"]
	if !ok {
		t.Fatalf("analiz aşaması meter'da yok: %+v", snap)
	}
	if got.Calls != 1 || got.PromptTokens != 100 || got.CompletionTokens != 20 || got.TotalTokens != 120 {
		t.Errorf("usage yanlış kaydedildi: %+v", got)
	}
}

// TestUsageRetryCountsOnce: aynı mantıksal çağrı 429 ile 1 kez başarısız
// olup 2. denemede başarılı olursa yalnız BAŞARILI cevabın usage'ı eklenir
// — 1 çağrı sayılır, retry'nin kendisi ayrı bir çağrı olarak sayılmaz.
func TestUsageRetryCountsOnce(t *testing.T) {
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
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	meter := NewUsageMeter()
	ctx := WithMeter(context.Background(), meter)
	if _, err := c.ChatJSON(ctx, "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("429 sonrası retry beklenirdi; çağrı sayısı: %d", calls.Load())
	}

	snap := meter.Snapshot()
	got := snap["diğer"] // etiketsiz -> "diğer"
	if got.Calls != 1 {
		t.Errorf("retry sonrası 1 çağrı beklenirdi, geldi: %d", got.Calls)
	}
	if got.TotalTokens != 15 {
		t.Errorf("yalnız başarılı cevabın usage'ı eklenmeli, geldi: %+v", got)
	}
}

// TestUsageFailedAttemptsNotCounted: 429/hata ile biten (tüm denemeler
// başarısız) çağrı token saymaz — meter'a hiç ekleme yapılmaz.
func TestUsageFailedAttemptsNotCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "bad", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	meter := NewUsageMeter()
	ctx := WithMeter(context.Background(), meter)
	if _, err := c.ChatJSON(ctx, "s", "u"); err == nil {
		t.Fatal("401'de hata beklenir")
	}

	snap := meter.Snapshot()
	if len(snap) != 0 {
		t.Errorf("başarısız çağrı meter'a hiçbir şey eklememeli, geldi: %+v", snap)
	}
}

// TestUsageNoMeterIsNoop: ctx'te meter yoksa (testler, api/serve süreçleri)
// istemci hiçbir şey yapmaz — davranış birebir aynı kalır (panic yok, hata
// yok).
func TestUsageNoMeterIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
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
}

// TestUsageNoUsageInResponseCountsCallZeroTokens: cevapta usage alanı hiç
// yoksa çağrı yine sayılır, token 0 eklenir.
func TestUsageNoUsageInResponseCountsCallZeroTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	meter := NewUsageMeter()
	ctx := WithStage(WithMeter(context.Background(), meter), "sentez")
	if _, err := c.ChatJSON(ctx, "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}

	got := meter.Snapshot()["sentez"]
	if got.Calls != 1 {
		t.Errorf("usage yoksa da çağrı sayılmalı, geldi: %d", got.Calls)
	}
	if got.TotalTokens != 0 || got.PromptTokens != 0 || got.CompletionTokens != 0 {
		t.Errorf("usage yoksa token 0 olmalı, geldi: %+v", got)
	}
}

// TestUsageStageDefaultsToDiger: WithStage hiç çağrılmadıysa (etiketsiz)
// kayıt "diğer" aşamasına düşer.
func TestUsageStageDefaultsToDiger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	meter := NewUsageMeter()
	ctx := WithMeter(context.Background(), meter)
	if _, err := c.ChatJSON(ctx, "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}

	snap := meter.Snapshot()
	if _, ok := snap["diğer"]; !ok {
		t.Errorf("etiketsiz çağrı \"diğer\"e düşmeli: %+v", snap)
	}
}

// TestUsageMeterAddConcurrent: paralel Add çağrıları yarışsız birikir
// (go test -race ile doğrulanır, #144).
func TestUsageMeterAddConcurrent(t *testing.T) {
	m := NewUsageMeter()
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			m.Add("analiz", Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})
		}()
	}
	wg.Wait()

	got := m.Snapshot()["analiz"]
	if got.Calls != n {
		t.Errorf("çağrı sayısı: %d, beklenen: %d", got.Calls, n)
	}
	if got.TotalTokens != n*2 {
		t.Errorf("toplam token: %d, beklenen: %d", got.TotalTokens, n*2)
	}
}

// TestUsageMeterSnapshotIsCopy: Snapshot döndürdüğü haritanın değiştirilmesi
// meter'ın kendi durumunu etkilememeli.
func TestUsageMeterSnapshotIsCopy(t *testing.T) {
	m := NewUsageMeter()
	m.Add("analiz", Usage{TotalTokens: 10})
	snap := m.Snapshot()
	snap["analiz"] = StageUsage{TotalTokens: 9999}

	got := m.Snapshot()["analiz"]
	if got.TotalTokens != 10 {
		t.Errorf("Snapshot kopyası meter'ı etkilememeli, geldi: %+v", got)
	}
}
