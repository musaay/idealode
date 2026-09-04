package store

import (
	"encoding/json"
	"time"
)

// Source, DB-backed paylaşılan kaynak listesi satırı. Community alanı
// connector'a özgü selector'dır: Reddit -> subreddit, SE -> site slug,
// HN -> arama sorgusu, GitHub -> search query, PH -> topic slug.
type Source struct {
	ID          int64
	Platform    string
	Community   string
	Category    string
	Active      bool
	LastSeenRef string // boş olabilir (ilk çekim)
}

// RawPost, platform-agnostik ham içerik (raw_posts satırı).
type RawPost struct {
	ID        int64
	Platform  string
	SourceRef string // platformun kendi kimliği (HN objectID, SE question_id...)
	Community string
	Title     string
	Body      string
	Author    string
	URL       string
	Score     int
	CreatedAt time.Time
}

// PostAnalysis, Groq classification çıktısı (post_analysis satırı).
type PostAnalysis struct {
	PostID           int64
	Classification   string   // pain_point | feature_request | complaint | noise
	ProblemSummary   string   // OUTPUT_LANG (TR)
	TargetAudience   string   // OUTPUT_LANG (TR)
	DomainTags       []string // kanonik EN slug'lar
	WillingnessToPay bool
	Prefiltered      bool // TRUE ise LLM'e gitmeden keyword filtresi eledi
}

// Theme, tag bazlı gruplamanın ürünü (themes satırı).
type Theme struct {
	ID        int64
	Name      string
	Frequency int
}

// Idea, paylaşılan idea card "tohumu".
type Idea struct {
	ID                      int64      `json:"id"`
	Title                   string     `json:"title"`
	ProblemStatement        string     `json:"problem_statement"`
	ProposedSolution        string     `json:"proposed_solution"`
	TargetUser              string     `json:"target_user"`
	EvidenceCount           int        `json:"evidence_count"`
	ExampleQuotes           []string   `json:"example_quotes"` // orijinal dil (EN)
	SourceType              string     `json:"source_type"`
	SourceThemeID           *int64     `json:"source_theme_id,omitempty"`
	UrgencyScore            int        `json:"urgency_score"`       // 1-5
	MonetizationSignal      int        `json:"monetization_signal"` // 0-5 (0 = sinyal yok)
	KnownCompetitorsAIGuess string     `json:"known_competitors_ai_guess,omitempty"`
	DomainTags              []string   `json:"domain_tags"`
	LocalEvidence           []string   `json:"local_evidence"`           // füzyonla eşleşen yerel talep satırları (#43)
	ParentIdeaID            *int64     `json:"parent_idea_id,omitempty"` // ai_blended: türetildiği kart
	Mine                    bool       `json:"mine"`                     // ai_blended ve bu oturuma ait
	SourceTheme             string     `json:"source_theme,omitempty"`   // tema adı (dump görünümü)
	CreatedBySessionID      string     `json:"-"`                        // ai_blended: üreten anonim oturum (görünürlük kuralı; store'da hesaplanır: source_type='ai_blended' AND created_by_session_id = $sid)
	CreatedAt               time.Time  `json:"created_at"`
	FusedAt                 *time.Time `json:"fused_at,omitempty"` // ilk füzyon denemesi damgası; dolu+eski = haftalık ivme yeniden deneme adayı (#50 B)
}

// ChatMessage, kart sohbeti satırı (idea_conversations). Girişsiz kimlik
// anonim oturum çerezi (session_id) ile kurulur — user_id/session_id'den
// biri dolu olmalı (bkz. 010_anon_chat.sql CHECK constraint'i).
type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"` // user | assistant
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// MarshalJSON, ChatMessage'ı sözleşmedeki alan adlarıyla ve RFC3339 UTC
// zaman damgasıyla yazar.
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	type view struct {
		ID        int64  `json:"id"`
		Role      string `json:"role"`
		Message   string `json:"message"`
		CreatedAt string `json:"created_at"`
	}
	return json.Marshal(view{
		ID:        m.ID,
		Role:      m.Role,
		Message:   m.Message,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// BlendDraft, sohbetten türetilen yeni kartın LLM'den gelen alanları
// (copilot.Blend tarafından doğrulanıp doldurulur). Kanıt alanları
// (example_quotes/evidence_count/source_theme_id/local_evidence) burada
// YOK — bunlar LLM'den gelmez, kaynak karttan birebir kopyalanır
// (InsertBlendedIdea).
type BlendDraft struct {
	Title              string
	ProblemStatement   string
	ProposedSolution   string
	TargetUser         string
	DomainTags         []string
	UrgencyScore       int
	MonetizationSignal int
}

// IdeaSource, bir kartın arkasındaki kaynak gönderinin gösterime yetecek
// alanları (web kaynak listesi). Yazar/skor gibi alanlar bilinçli olarak
// taşınmaz — arayüzde gösterilmez.
type IdeaSource struct {
	Platform  string
	Community string
	URL       string
	CreatedAt time.Time
}

// MarshalJSON, IdeaSource'u API sözleşmesindeki alan adlarıyla yazar
// (platform/community/url/created_at). encoding/json'ın `omitempty`'si
// struct alanlarını (time.Time dahil) asla atlamadığı için sıfır zaman
// burada elle denetlenir — CreatedAt.IsZero() ise created_at hiç yazılmaz.
func (s IdeaSource) MarshalJSON() ([]byte, error) {
	type view struct {
		Platform  string `json:"platform"`
		Community string `json:"community"`
		URL       string `json:"url"`
		CreatedAt string `json:"created_at,omitempty"`
	}
	v := view{Platform: s.Platform, Community: s.Community, URL: s.URL}
	if !s.CreatedAt.IsZero() {
		v.CreatedAt = s.CreatedAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(v)
}
