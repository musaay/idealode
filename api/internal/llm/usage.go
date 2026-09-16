// usage.go: Groq/OpenAI-uyumlu cevaplardaki `usage` alanını aşama bazlı
// ölçen bileşenler (#144). Bu dosya YALNIZ ölçer — hiçbir davranış
// değiştirmez: ctx'te meter yoksa istemci hiçbir şey yapmaz.
package llm

import (
	"context"
	"sync"
)

// Usage, sağlayıcı cevabındaki token sayaçları (OpenAI chat-completions
// şeması — Groq da aynı alanları döner).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StageUsage, tek bir aşamanın (analiz, kümeleme, sentez, ...) birikmiş
// çağrı sayısı + token toplamı.
type StageUsage struct {
	Calls            int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// unlabeledStage, çağrı noktasında llm.WithStage ile etiketlenmemiş
// çağrıların düştüğü aşama adı.
const unlabeledStage = "diğer"

// UsageMeter, bir `run` koşusu boyunca aşama başına token/çağrı sayısını
// eşzamanlılığa güvenli biriktirir.
type UsageMeter struct {
	mu     sync.Mutex
	stages map[string]StageUsage
}

// NewUsageMeter boş bir meter döner.
func NewUsageMeter() *UsageMeter {
	return &UsageMeter{stages: make(map[string]StageUsage)}
}

// Add, stage'in birikmiş sayaçlarına 1 çağrı + u'nun token'larını ekler.
// Boş aşama adı unlabeledStage'e ("diğer") düşer.
func (m *UsageMeter) Add(stage string, u Usage) {
	if stage == "" {
		stage = unlabeledStage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stages[stage]
	s.Calls++
	s.PromptTokens += u.PromptTokens
	s.CompletionTokens += u.CompletionTokens
	s.TotalTokens += u.TotalTokens
	m.stages[stage] = s
}

// Snapshot, o ana kadar biriken aşama->StageUsage haritasının bağımsız bir
// kopyasını döner (çağıran döndürülen haritayı değiştirse meter etkilenmez).
func (m *UsageMeter) Snapshot() map[string]StageUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]StageUsage, len(m.stages))
	for k, v := range m.stages {
		out[k] = v
	}
	return out
}

// contextKey, context değerleri için çakışmayan anahtar tipidir.
type contextKey int

const (
	meterContextKey contextKey = iota
	stageContextKey
)

// WithMeter, ctx'e bir UsageMeter iliştirir — istemci yalnız bu ctx'ten
// türeyen çağrılarda usage biriktirir.
func WithMeter(ctx context.Context, m *UsageMeter) context.Context {
	return context.WithValue(ctx, meterContextKey, m)
}

// WithStage, ctx'e geçerli aşama adını iliştirir (çağrı noktasında verilir).
func WithStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, stageContextKey, stage)
}

// meterFromContext, ctx'e iliştirilmiş meter'ı döner; yoksa nil (testler,
// api/serve süreçleri — davranış birebir aynı kalır).
func meterFromContext(ctx context.Context) *UsageMeter {
	m, _ := ctx.Value(meterContextKey).(*UsageMeter)
	return m
}

// stageFromContext, ctx'e iliştirilmiş aşama adını döner; yoksa/boşsa
// unlabeledStage ("diğer").
func stageFromContext(ctx context.Context) string {
	s, _ := ctx.Value(stageContextKey).(string)
	if s == "" {
		return unlabeledStage
	}
	return s
}

// recordUsage, ctx'te meter varsa başarılı cevabın usage'ını geçerli
// aşamaya ekler; meter yoksa no-op.
func recordUsage(ctx context.Context, u Usage) {
	m := meterFromContext(ctx)
	if m == nil {
		return
	}
	m.Add(stageFromContext(ctx), u)
}
