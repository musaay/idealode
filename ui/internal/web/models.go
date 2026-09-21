package web

import (
	"errors"
	"time"
)

// Bu dosya, backend paketinin (store katmanı) JSON API üzerinden ürettiği
// sözleşmeyi ui tarafında birebir yansıtır. ui modülü backend'i import
// ETMEZ (ayrı Go modülü, ayrı deploy) — bu yüzden backend'deki
// Idea/IdeaSource/IdeaFilter/ErrNotFound/FlagDoubtful türlerinin tel
// üzerindeki (JSON) biçimiyle eşleşen kendi kopyalarını tanımlar. Alan
// adları/JSON etiketleri backend'in model tanımları ve api handler'larının
// ürettiği JSON ile birebir olmalı; sözleşme kopukluğuna karşı backend
// tarafındaki golden test bu paketin testdata/ altındaki fixture'larını
// üretir/doğrular (golden).

// Idea, `GET /api/ideas`, `GET /api/ideas/{slug}` ve
// `POST /api/ideas/{slug}/blend` yanıtlarındaki kart biçimi — backend'in
// store.Idea'sının JSON izdüşümü.
type Idea struct {
	ID                      int64      `json:"id"`
	Slug                    string     `json:"slug"`
	Title                   string     `json:"title"`
	ProblemStatement        string     `json:"problem_statement"`
	ProposedSolution        string     `json:"proposed_solution"`
	TargetUser              string     `json:"target_user"`
	EvidenceCount           int        `json:"evidence_count"`
	ExampleQuotes           []string   `json:"example_quotes"`
	SourceType              string     `json:"source_type"`
	SourceThemeID           *int64     `json:"source_theme_id,omitempty"`
	UrgencyScore            int        `json:"urgency_score"`
	MonetizationSignal      int        `json:"monetization_signal"`
	KnownCompetitorsAIGuess string     `json:"known_competitors_ai_guess,omitempty"`
	DomainTags              []string   `json:"domain_tags"`
	LocalEvidence           []string   `json:"local_evidence"`
	ParentIdeaID            *int64     `json:"parent_idea_id,omitempty"`
	ParentSlug              string     `json:"parent_slug,omitempty"`
	Mine                    bool       `json:"mine"`
	SourceTheme             string     `json:"source_theme,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	FusedAt                 *time.Time `json:"fused_at,omitempty"`

	// Özgünlük merceği (#101 v3) — ADVISORY. NULL = mercek hiç çağrılamadı
	// ya da bu alanları taşımayan kart türü (ai_blended: hep NULL).
	DistinctivenessVerdict   *string `json:"distinctiveness_verdict,omitempty"`
	DistinctivenessCriterion *string `json:"distinctiveness_criterion,omitempty"`
	DistinctivenessReason    *string `json:"distinctiveness_reason,omitempty"`

	// Veri-erişimi merceği (#131) — BLOCKING; "fail" burada asla görülmez
	// (bkz. backend store katmanındaki aynı alanın açıklaması).
	DataAccessVerdict *string `json:"data_access_verdict,omitempty"`
	DataAccessReason  *string `json:"data_access_reason,omitempty"`

	// LensVerdicts (#164): tüm mercek çağrılarının kalıcı denetim kaydı;
	// ASLA nil değil — kayıt yoksa boş dizi.
	LensVerdicts []LensVerdict `json:"lens_verdicts,omitempty"`

	// PublishedAt, moderasyon kuyruğu damgası (#102): NULL = beklemede.
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// LensVerdict, Idea.LensVerdicts'in tel üzerindeki eleman biçimi — backend'in
// store.LensVerdict'inin JSON izdüşümü.
type LensVerdict struct {
	Lens          string    `json:"lens"`
	PromptVersion string    `json:"prompt_version"`
	Subject       string    `json:"subject"`
	Verdict       string    `json:"verdict"`
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
}

// IdeaSource, `GET /api/ideas/{slug}/sources` yanıtındaki tek kaynak
// gönderi — backend'in store.IdeaSource'unun (özel MarshalJSON ile yazdığı)
// tel biçimi: platform/community/url/created_at.
type IdeaSource struct {
	Platform  string    `json:"platform"`
	Community string    `json:"community"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// IdeaFilter, galeri listelemesinin filtreleri (apiclient bunu `GET
// /api/ideas` sorgu dizesine çevirir). SessionID backend-only'dir (store
// sorgusunda ai_blended görünürlüğü için kullanılır, tel üzerinde
// taşınmaz — oturum kimliği ayrıca X-Session-Id başlığıyla gider) ve
// bilinçli olarak burada YOK.
type IdeaFilter struct {
	SourceType string
	Query      string
	Limit      int
	Flag       string
}

// FlagDoubtful, IdeaFilter.Flag'in tek geçerli değeri: özgünlük merceğinin
// (#101, advisory) fail ya da unsure dediği kartlar. Backend'deki
// store.FlagDoubtful ile birebir aynı sabit — beyaz liste iki modülde de
// bu değere bakar.
const FlagDoubtful = "doubtful"

// ErrNotFound, backend'in store.ErrNotFound'ına karşılık gelen tipli hata:
// apiclient 404'ü bu hataya çevirir, handler errors.Is ile ayırıp 404
// sayfası render eder.
var ErrNotFound = errors.New("kayıt bulunamadı")
