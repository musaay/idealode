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

// PostAnalysis, LLM classification çıktısı (post_analysis satırı).
type PostAnalysis struct {
	PostID           int64
	Classification   string   // pain_point | feature_request | complaint | noise
	ProblemSummary   string   // OUTPUT_LANG (TR)
	TargetAudience   string   // OUTPUT_LANG (TR)
	DomainTags       []string // kanonik EN slug'lar
	WillingnessToPay bool
	Prefiltered      bool // TRUE ise LLM'e gitmeden keyword filtresi eledi

	// Title/Body: yalnız UnthemedAnalyses'in JOIN'i doldurur (#127) — kova
	// içi LLM kümeleme prompt'u gönderi içeriğine ihtiyaç duyar.
	// post_analysis tablosunda YOK; InsertPostAnalyses bu alanları
	// yazmaz/okumaz.
	Title string
	Body  string
}

// Theme, tag bazlı gruplamanın ürünü (themes satırı).
type Theme struct {
	ID        int64
	Name      string
	Frequency int
	// HasPaymentSignal, ThemesReadyForSynthesis'in doldurduğu bilgi alanı:
	// temanın postlarından en az biri willingness_to_pay=true mu (#125).
	// Sert eleme değil, sıralama/loglama için kullanılır.
	HasPaymentSignal bool
	// DomainTag, temanın doğduğu kaba kova (#127) — ThemesByDomainTag'in
	// doldurduğu alan; kova içi LLM kümelemesine bağlam olarak verilir.
	DomainTag string
	// Clustered, ThemesReadyForSynthesis'in doldurduğu bilgi alanı: tema
	// gerçek dert kümelemesinden mi doğdu (theme_name != domain_tag) yoksa
	// eski etiket teması mı (theme_name == domain_tag, veya domain_tag NULL)
	// (#135). Sıralama/loglama için kullanılır.
	Clustered bool
}

// Idea, paylaşılan idea card "tohumu".
type Idea struct {
	ID                      int64      `json:"id"`
	Slug                    string     `json:"slug"` // URL-güvenli kimlik (#110); id dedup/log için ayrıca kalır
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
	ParentSlug              string     `json:"parent_slug,omitempty"`    // parent'ın slug'ı (ideaSelect self-join'i, #110); parent yoksa boş
	Mine                    bool       `json:"mine"`                     // ai_blended ve bu oturuma ait
	SourceTheme             string     `json:"source_theme,omitempty"`   // tema adı (dump görünümü)
	CreatedBySessionID      string     `json:"-"`                        // ai_blended: üreten anonim oturum (görünürlük kuralı; store'da hesaplanır: source_type='ai_blended' AND created_by_session_id = $sid)
	CreatedAt               time.Time  `json:"created_at"`
	FusedAt                 *time.Time `json:"fused_at,omitempty"` // ilk füzyon denemesi damgası; dolu+eski = haftalık ivme yeniden deneme adayı (#50 B)

	// Özgünlük merceği (#101 v3) — ADVISORY: kart üretimini bloklamaz, yalnız
	// işaretler. NULL = mercek hiç çağrılamadı (geçici hata) ya da bu alanları
	// taşımayan kart türü (ai_blended: kaynak karttan kopyalanmaz, hep NULL).
	DistinctivenessVerdict   *string `json:"distinctiveness_verdict,omitempty"`   // pass | fail | unsure | NULL
	DistinctivenessCriterion *string `json:"distinctiveness_criterion,omitempty"` // K1 | K2 | K3 | K4 | none | NULL
	DistinctivenessReason    *string `json:"distinctiveness_reason,omitempty"`

	// Veri-erişimi merceği (#131) — BLOCKING: yalnız "fail" kart üretimini
	// engeller (bkz. pipeline.blockedByIdeaLens / seeds.ProcessSeeds), bu
	// yüzden burada "fail" asla görülmez. "unsure" HİÇBİR yolda ENGELLEMEZ
	// (organik/pain_point VE tohum/market_derived-momentum_derived — ikisi
	// de aynı ilke, #131 PO düzeltmesi: sıcaklık 0 olduğundan unsure
	// deterministiktir, "yeniden dene" mantığı burada işlemez), yalnız
	// işaretler. NULL = mercek hiç çağrılamadı (geçici hata) ya da bu
	// alanları taşımayan kart türü (yalnız ai_blended — kaynak karttan
	// kopyalanmaz, hep NULL, distinctiveness_* ile aynı desen).
	DataAccessVerdict *string `json:"data_access_verdict,omitempty"` // pass | unsure | NULL
	DataAccessReason  *string `json:"data_access_reason,omitempty"`

	// LensVerdicts (#164): kartı üreten/etkileyen TÜM mercek çağrılarının
	// (üçüncü-taraf/veri-erişimi/pazar-işlerliği/özgünlük, ivme tohumunda +
	// ürünleştirilebilirlik) kalıcı denetim kaydı — pass/fail/unsure/error
	// dahil HEPSİ (yalnız blocking "fail" değil). ai_blended kartlarda
	// (kaynak karttan kopyalanmaz) ve bu alanı taşımayan eski satırlarda
	// boş dizi ([]) — ASLA nil/NULL (NOT NULL DEFAULT '[]').
	LensVerdicts []LensVerdict `json:"lens_verdicts,omitempty"`

	// PublishedAt, moderasyon kuyruğu damgası (#102): NULL = beklemede (PO
	// onayı yok, herkese açık galeri/detayda görünmez); dolu = yayında.
	// `dump` (ListIdeas, DB'ye dokunan lead aracı) bekleyenleri de döner —
	// bu alan sayesinde lead hangilerinin beklemede olduğunu ayırt eder.
	PublishedAt *time.Time `json:"published_at,omitempty"`
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

// Elimination, eliminations tablosu satırı (#138): bir tema/tohum/kartın
// pipeline'dan neden düştüğünün kalıcı denetim izi. Doygunluk merceğinin
// (K1) artık BLOKLAYICI olmasının tek riski "görülmeyen şey denetlenemez"
// idi — bu tablo o riski kapatır.
type Elimination struct {
	ID         int64
	OccurredAt time.Time
	// Stage: incoherent_theme | blocking_lens | distinctiveness |
	// vendor_internal | payment_gate (son değer şemada var ama şu an
	// yazılmıyor — ileride kullanılabilir diye önden izin verir).
	Stage string
	// Subject: tema adı, tohum adı ya da üretilmiş kart başlığı — hangisi
	// o an elde varsa.
	Subject string
	// Verdict: şimdilik hep "fail" — satır yalnız elemede yazılır.
	Verdict string
	// Criterion: yalnız stage=distinctiveness satırlarında dolu (K1-K4);
	// diğer stage'lerde NULL (sütun o stage'lerde anlamsız).
	Criterion *string
	// Reason: LLM'in tek cümlelik gerekçesi ya da (incoherent_theme için)
	// tutarlılık oran metni; NULL olabilir.
	Reason *string
	// Detail: subject tek başına yetersiz kaldığında (özellikle tema/kova
	// adları) PO'ya "ne elendi" bağlamını taşır — kart varsa problem
	// cümlesi, tohumdan geldiyse özeti, ikisi de yoksa en güçlü kanıtın
	// başlığı; hiçbiri yoksa NULL.
	Detail *string
	// Check (#164): elemeyi yapan merceğin adı (stage=blocking_lens/
	// distinctiveness satırlarında dolu — "üçüncü-taraf inşa edilebilirlik",
	// "veri-erişimi", "özgünlük" gibi); mercek-dışı elemelerde (incoherent_
	// theme, vendor_internal) NULL — bu satırların "check"i yok. "check"
	// Postgres'te ayrılmış anahtar sözcük, kolon adı DAİMA tırnaklı
	// kullanılır (bkz. queries.go).
	Check *string
	// Verdicts (#164): kartı üreten/etkileyen TÜM mercek çağrılarının
	// (pass/fail/unsure/error) kalıcı kaydı — kart hiç yazılmadan elenen
	// durumda (K1 doygunluk ya da bloklayıcı mercek fail'i) tek kalıcı
	// yeri burasıdır; kart yazılan durumda ideas.lens_verdicts'in AYNISI.
	// nil ASLA yazılmaz — boş dizi ([]LensVerdict{}) yazılır (NOT NULL
	// DEFAULT '[]').
	Verdicts []LensVerdict
}

// LensVerdict, ideas.lens_verdicts / eliminations.verdicts jsonb
// kolonlarının eleman şeması (#164): bir mercek ÇAĞRISININ (başarılı ya da
// hatalı) kalıcı kaydı. Çağrı hatası (429/413/400/ağ) Verdict="error" +
// Reason=hata metniyle (500 rune'a kırpılmış) yazılır — NULL (hiç
// çağrılmadı) ile karışmasın diye.
type LensVerdict struct {
	// Lens: merceğin Türkçe adı (seedLens.name ile birebir — "üçüncü-taraf
	// inşa edilebilirlik", "veri-erişimi", "pazar-işlerliği", "özgünlük",
	// "ürünleştirilebilirlik").
	Lens string `json:"lens"`
	// PromptVersion: mercek sabitinin yanındaki lensXVersion (şu an hepsi
	// "v1" — sonraki v3 işlerinde artar, #163 §6).
	PromptVersion string `json:"prompt_version"`
	// Subject: mercek HANGİ girdi üzerinde çalıştı — "card" (üretilmiş kart
	// alanları) ya da "seed" (ham tohum alanları). Organik yolda TÜMÜ
	// "card"; tohum yolunda 3(-4) bloklayıcı mercek "seed", özgünlük "card".
	Subject string `json:"subject"`
	// Verdict: pass | fail | unsure | error.
	Verdict string    `json:"verdict"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}
