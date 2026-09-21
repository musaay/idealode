package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

// newUsageProbeChat, gerçek llm.OpenAICompatClient'ı bir httptest sunucusuna
// karşı döner (#144) — sahte Chat'ler recordUsage'ı hiç tetiklemediğinden
// (llm.OpenAICompatClient'ın kendi iç mekanizması), pipeline çağrı
// noktalarındaki llm.WithStage etiketinin gerçek meter'a doğru aşamayla
// ulaştığını uçtan uca doğrulamak için GERÇEK istemci kullanılır — DB/Groq'a
// dokunmaz, yalnız yerel httptest sunucusuna.
func newUsageProbeChat(t *testing.T, response string) llm.Chat {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": response}}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return &llm.OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
}

// TestStagePropagationCoherentSubset, synthesize.go'daki
// `coherentSubset(llm.WithStage(ctx, "tutarlılık"), ...)` çağrı noktasının
// usage'ı doğru aşamaya yazdırdığını doğrular (#144).
func TestStagePropagationCoherentSubset(t *testing.T) {
	chat := newUsageProbeChat(t, `{"indices":[0]}`)
	meter := llm.NewUsageMeter()
	ctx := llm.WithStage(llm.WithMeter(context.Background(), meter), "tutarlılık")

	if _, err := coherentSubset(ctx, chat, []store.RawPost{{Title: "a", Body: "b"}}); err != nil {
		t.Fatalf("coherentSubset: %v", err)
	}

	got := meter.Snapshot()["tutarlılık"]
	if got.Calls != 1 || got.TotalTokens != 10 {
		t.Errorf("tutarlılık aşaması beklenen usage'ı almadı: %+v", got)
	}
}

// TestStagePropagationBlockedByIdeaLens, synthesize.go'daki
// `blockedByIdeaLens(llm.WithStage(ctx, "mercek"), ...)` çağrı noktasının
// üç mercek çağrısının TÜMÜNÜ "mercek" aşamasına yazdırdığını doğrular.
func TestStagePropagationBlockedByIdeaLens(t *testing.T) {
	chat := newUsageProbeChat(t, `{"verdict":"pass","reason":"ok"}`)
	meter := llm.NewUsageMeter()
	ctx := llm.WithStage(llm.WithMeter(context.Background(), meter), "mercek")
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	if _, _, blocked := blockedByIdeaLens(ctx, chat, idea); blocked {
		t.Fatal("pass verdict'te bloklanmamalı")
	}

	got := meter.Snapshot()["mercek"]
	if got.Calls != len(seedLenses) {
		t.Errorf("mercek aşamasında %d çağrı beklenirdi, geldi: %d", len(seedLenses), got.Calls)
	}
	if got.TotalTokens != len(seedLenses)*10 {
		t.Errorf("mercek aşaması token toplamı yanlış: %+v", got)
	}
}

// TestStagePropagationDistinctivenessCheck, seeds.go/synthesize.go'daki
// `distinctivenessCheck(llm.WithStage(ctx, "özgünlük"), ...)` çağrı
// noktasının usage'ı doğru aşamaya yazdırdığını doğrular.
func TestStagePropagationDistinctivenessCheck(t *testing.T) {
	chat := newUsageProbeChat(t, `{"verdict":"pass","criterion":"none","reason":"ok"}`)
	meter := llm.NewUsageMeter()
	ctx := llm.WithStage(llm.WithMeter(context.Background(), meter), "özgünlük")
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	if err := distinctivenessCheck(ctx, chat, idea); err != nil {
		t.Fatalf("distinctivenessCheck: %v", err)
	}

	got := meter.Snapshot()["özgünlük"]
	if got.Calls != 1 || got.TotalTokens != 10 {
		t.Errorf("özgünlük aşaması beklenen usage'ı almadı: %+v", got)
	}
}

// TestStagePropagationSynthesizeOne, synthesize.go'daki
// `synthesizeOne(llm.WithStage(ctx, "sentez"), ...)` çağrı noktasının
// usage'ı doğru aşamaya yazdırdığını doğrular.
func TestStagePropagationSynthesizeOne(t *testing.T) {
	chat := newUsageProbeChat(t, `{"title":"Başlık Yeterince Uzun","problem_statement":"sorun",
		"proposed_solution":"çözüm","target_user":"kullanıcı","domain_tags":["x"]}`)
	meter := llm.NewUsageMeter()
	ctx := llm.WithStage(llm.WithMeter(context.Background(), meter), "sentez")
	cfg := &config.Config{OutputLang: "tr"}
	th := store.Theme{ID: 1, Name: "test-tag", Frequency: 3}

	if _, err := synthesizeOne(ctx, cfg, chat, th, []store.RawPost{{Title: "a", Body: "b"}}); err != nil {
		t.Fatalf("synthesizeOne: %v", err)
	}

	got := meter.Snapshot()["sentez"]
	if got.Calls != 1 || got.TotalTokens != 10 {
		t.Errorf("sentez aşaması beklenen usage'ı almadı: %+v", got)
	}
}
