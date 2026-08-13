package store

import "time"

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
	SourceTheme             string     `json:"source_theme,omitempty"` // tema adı (dump görünümü)
	CreatedAt               time.Time  `json:"created_at"`
}
