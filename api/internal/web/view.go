package web

import (
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// dateLayout, arayüzde gösterilen tek tarih biçimi (dile göre değişmez).
const dateLayout = "2006-01-02"

// formatDate, tarihi YYYY-MM-DD basar; sıfır ya da inandırıcı olmayan bir
// zaman damgası (radar_seed satırları platformda yayın zamanı taşımaz, Go
// sıfır zamanıyla yazılır) hiç basılmaz — uydurma tarih gösterilmez.
func formatDate(t time.Time) string {
	if t.IsZero() || t.Year() < 1970 {
		return ""
	}
	return t.Format(dateLayout)
}

// Page, her şablonun ortak tabanı. Şablonlar metni `{{ .T "anahtar" }}` ile
// alır — funcMap'e dil bağlanamaz (şablonlar istek başına değil, başlangıçta
// bir kez parse edilir), bu yüzden çevirici veri modelinde taşınır.
type Page struct {
	Lang       string // "tr" | "en"
	Theme      string // "", "light", "dark" — boşsa prefers-color-scheme geçerli
	Title      string // <title> içeriği (zaten çevrilmiş)
	AssetVer   string // statik dosya sürüm damgası (cache busting)
	LangLinkTR string // mevcut sayfanın TR bağlantısı (JS'siz dil anahtarı)
	LangLinkEN string
	ThemeLink  string // karşı temaya geçiren bağlantı
	NextTheme  string // "light" | "dark" — ThemeLink'in götürdüğü tema

	// Kabuk (sol nav / mobil header / breadcrumb) durumu.
	MobileTitle string // mobil üst header başlığı
	ShowBack    bool   // mobil header ve breadcrumb'da geri oku
	Breadcrumb  string // masaüstü üst çubukta kısaltılmış kart başlığı
	NavCount    string // sol nav "Galeri" rozetindeki sayı; bilinmiyorsa boş
	OnGallery   bool   // galeri sayfasındayız (nav "current" işareti)

	// Copilot girişleri (sol nav ikinci sekmesi, üst çubuk düğmesi, mobil
	// alt nav sekmesi). Kart detayında sohbet paneline götürür; galeride
	// pasif "Yakında" olarak kalır (kartsız global sohbet henüz yok).
	CopilotActive bool
}

// T, katalogdan çeviri döner; args verilirse fmt.Sprintf uygulanır.
func (p Page) T(key string, args ...any) string { return translate(p.Lang, key, args...) }

// IsDark, seçili temanın koyu olup olmadığını söyler (buton etiketleri için).
func (p Page) IsDark() bool { return p.Theme == "dark" }

// breadcrumbMax, masaüstü üst çubuktaki kart başlığının kırpma sınırı.
const breadcrumbMax = 32

// clipTitle, breadcrumb için başlığı rune sınırında kırpar. Kart METNİ değil,
// yalnız gezinme etiketi kırpılır — içerik hiçbir yerde kısaltılmaz.
func clipTitle(s string) string {
	r := []rune(s)
	if len(r) <= breadcrumbMax {
		return s
	}
	return strings.TrimSpace(string(r[:breadcrumbMax])) + "…"
}

// Badge, kart rozetinin görünen hâli. Kind CSS sınıfına, Label metne gider.
type Badge struct {
	Kind  string // pain_point | market_derived | ai_blended | ai_generated | user_created | other
	Label string
}

// knownPlatforms, kataloğda insan-okur karşılığı olan platform anahtarları.
// Listede olmayan platform HAM değeriyle gösterilir — uydurma etiket yok.
var knownPlatforms = map[string]bool{
	"radar_seed":      true,
	"hackernews":      true,
	"stackexchange":   true,
	"googleplay":      true,
	"producthunt":     true,
	"technopat":       true,
	"github":          true,
	"github_trending": true,
	"reddit":          true,
}

// platformLabel, platform anahtarını gösterim etiketine çevirir.
func platformLabel(lang, platform string) string {
	if knownPlatforms[platform] {
		return translate(lang, "platform."+platform)
	}
	return platform
}

// knownSourceTypes, kataloğda karşılığı olan kaynak türleri.
var knownSourceTypes = map[string]bool{
	"pain_point":     true,
	"market_derived": true,
	"ai_blended":     true,
	"ai_generated":   true,
	"user_created":   true,
}

// badgeFor, source_type'ı rozete çevirir. Bilinmeyen tür nötr gösterilir ve
// HAM değeriyle yazılır — uydurma etiket üretilmez.
func badgeFor(lang, sourceType string) Badge {
	if knownSourceTypes[sourceType] {
		return Badge{Kind: sourceType, Label: translate(lang, "source_type."+sourceType)}
	}
	return Badge{Kind: "other", Label: sourceType}
}

// IdeaCard, galeri ızgarasındaki tek kart.
type IdeaCard struct {
	ID            int64
	Title         string
	Problem       string
	Badge         Badge
	EvidenceCount int
	DomainTags    []string
	CreatedAt     string // YYYY-MM-DD
	Href          string
}

// FilterChip, galeri filtre bağlantısı (JS'siz çalışır: düz <a href>).
type FilterChip struct {
	Label  string
	Href   string
	Kind   string // rozet renkleriyle aynı anahtar; "all" nötr
	Active bool
}

// GalleryPage, `GET /` görünüm modeli.
type GalleryPage struct {
	Page
	Ideas      []IdeaCard
	Chips      []FilterChip
	Query      string
	SourceType string
	Count      int
	Filtered   bool // arama veya filtre uygulanmış mı (boş durumda "temizle")
}

// EvidenceItem, yerel talep satırının ayrıştırılmış hâli. URL boşsa satır
// yalnız alıntı olarak gösterilir.
type EvidenceItem struct {
	Quote    string
	Platform string
	URL      string
	Host     string
}

// SourceLink, kart detayındaki kaynak gönderi satırı.
type SourceLink struct {
	Platform  string
	Community string
	URL       string
	Host      string
	Date      string
}

// IdeaPage, `GET /ideas/{id}` görünüm modeli.
type IdeaPage struct {
	Page
	ID            int64
	Heading       string
	Badge         Badge
	Problem       string
	Solution      string
	TargetUser    string
	Quotes        []string // birebir, kırpılmadan
	EvidenceCount int
	LocalEvidence []EvidenceItem
	Sources       []SourceLink
	DomainTags    []string
	SourceTheme   string
	Competitors   string
	Urgency       int
	Monetization  int
	CreatedAt     string
	HasMeta       bool // meta paneli hiç satır taşımıyorsa hiç basılmaz

	// Sohbet (dilim 2).
	ParentHref string // ai_blended kartın kaynak kartı; boşsa satır basılmaz
	Mine       bool   // yalnız bu oturuma görünen kart
	Chat       ChatPanel
}

// ChatBubble, panelde basılan tek mesaj.
type ChatBubble struct {
	IsUser bool
	Body   template.HTML // escape SONRASI satır sonları <br> yapılmış metin
	Time   string        // HH:MM (yerel biçim yok; UTC damgası kısaltılır)
}

// QuickPrompt, panelin üstündeki sabit hızlı komut çipi. JS'siz de çalışır:
// çip, forma ait `name="message"` taşıyan bir submit düğmesidir.
type QuickPrompt struct {
	Label string
	Query string
}

// ChatPanel, kart sohbeti panelinin görünüm modeli.
type ChatPanel struct {
	IdeaID       int64
	PostHref     string // POST hedefi (sohbet)
	BlendHref    string // POST hedefi (kart olarak türet)
	Title        string // "<kısaltılmış kart adı>… Copilot"
	Messages     []ChatBubble
	QuickPrompts []QuickPrompt
	Error        string // gösterilecek hata metni (boşsa satır basılmaz)
	Lang         string
	MaxLen       int
}

// chatTitleMax, panel başlığındaki kart adının kırpma sınırı (referans:
// idea.title.slice(0, 20) + "... Copilot").
const chatTitleMax = 20

// clipChatTitle, panel başlığı için kart adını kırpar.
func clipChatTitle(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= chatTitleMax {
		return string(r)
	}
	return strings.TrimSpace(string(r[:chatTitleMax])) + "…"
}

// messageBody, sohbet mesajını güvenle HTML'e çevirir: ÖNCE tam escape,
// SONRA satır sonu -> <br>. Sıra kritiktir — ham metin hiçbir aşamada
// HTML olarak yorumlanmaz, yalnız satır yapısı korunur. LLM ve kullanıcı
// metni bu yüzden `template.HTML` olarak basılsa da zararsızdır.
func messageBody(s string) template.HTML {
	esc := template.HTMLEscapeString(s)
	esc = strings.ReplaceAll(esc, "\r\n", "\n")
	esc = strings.ReplaceAll(esc, "\r", "\n")
	esc = strings.ReplaceAll(esc, "\n", "<br>")
	return template.HTML(esc) //nolint:gosec // içerik yukarıda escape edildi
}

// chatMaxLen, giriş alanının karakter tavanı (API sözleşmesiyle aynı).
const chatMaxLen = 1000

// buildChatPanel, geçmişi ve sabit çipleri panel modeline çevirir.
func buildChatPanel(lang string, ideaID int64, title string, msgs []ChatMessage, errMsg string) ChatPanel {
	bubbles := make([]ChatBubble, 0, len(msgs))
	for _, m := range msgs {
		bubbles = append(bubbles, ChatBubble{
			IsUser: m.Role == "user",
			Body:   messageBody(m.Message),
			Time:   formatClock(m.CreatedAt),
		})
	}
	return ChatPanel{
		IdeaID:    ideaID,
		PostHref:  fmt.Sprintf("/ideas/%d/chat", ideaID),
		BlendHref: fmt.Sprintf("/ideas/%d/blend", ideaID),
		Title:     translate(lang, "chat.title", clipChatTitle(title)),
		Messages:  bubbles,
		QuickPrompts: []QuickPrompt{
			{Label: translate(lang, "chat.quick.arch"), Query: translate(lang, "chat.quick.arch_query")},
			{Label: translate(lang, "chat.quick.market"), Query: translate(lang, "chat.quick.market_query")},
			{Label: translate(lang, "chat.quick.risks"), Query: translate(lang, "chat.quick.risks_query")},
		},
		Error:  errMsg,
		Lang:   lang,
		MaxLen: chatMaxLen,
	}
}

// formatClock, mesaj zaman damgasını HH:MM basar; sıfır zaman boş döner.
func formatClock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("15:04")
}

// ErrorPage, 404/500 görünüm modeli.
type ErrorPage struct {
	Page
	Status  int
	Heading string
	Message string
}

// localEvidenceRe, `<alıntı> — [<platform>] <url>` satırını ayrıştırır.
// Alıntı greedy'dir: alıntının içinde de " — [" geçse son ayraç kazanır.
var localEvidenceRe = regexp.MustCompile(`^(.*)\s+—\s+\[([^\]]*)\]\s*(\S*)\s*$`)

// parseLocalEvidence, fuse adımının yazdığı satırı (bkz. pipeline/fuse.go)
// alıntı + platform + URL'ye ayırır. Ayrıştırılamayan satır OLDUĞU GİBİ
// alıntı sayılır — kanıt asla düşürülmez ya da değiştirilmez.
func parseLocalEvidence(line string) EvidenceItem {
	raw := strings.TrimSpace(line)
	if raw == "" {
		return EvidenceItem{}
	}
	m := localEvidenceRe.FindStringSubmatch(raw)
	if m == nil {
		return EvidenceItem{Quote: raw}
	}
	quote := strings.TrimSpace(m[1])
	if quote == "" {
		return EvidenceItem{Quote: raw}
	}
	item := EvidenceItem{Quote: quote, Platform: strings.TrimSpace(m[2])}
	if u := safeURL(m[3]); u != "" {
		item.URL = u
		item.Host = hostOf(u)
	}
	return item
}

// parseLocalEvidenceList, boş satırları eleyerek listeyi ayrıştırır ve
// platform anahtarını gösterim etiketine çevirir (kaynak listesiyle aynı
// eşleme; alıntı metnine dokunulmaz).
func parseLocalEvidenceList(lang string, lines []string) []EvidenceItem {
	out := make([]EvidenceItem, 0, len(lines))
	for _, l := range lines {
		it := parseLocalEvidence(l)
		if it.Quote == "" {
			continue
		}
		it.Platform = platformLabel(lang, it.Platform)
		out = append(out, it)
	}
	return out
}

// safeURL, yalnız http/https mutlak URL'leri geçirir; javascript:, data: ve
// bozuk değerler boş döner (şablonda hiç link basılmaz).
func safeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	return u.String()
}

// hostOf, gösterim için kısaltılmış konak adı ("www." atılır).
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// buildGallery, store satırlarını galeri görünümüne çevirir.
func buildGallery(base Page, ideas []store.Idea, sourceType, query string) GalleryPage {
	cards := make([]IdeaCard, 0, len(ideas))
	for _, i := range ideas {
		cards = append(cards, IdeaCard{
			ID:            i.ID,
			Title:         i.Title,
			Problem:       i.ProblemStatement,
			Badge:         badgeFor(base.Lang, i.SourceType),
			EvidenceCount: i.EvidenceCount,
			DomainTags:    i.DomainTags,
			CreatedAt:     formatDate(i.CreatedAt),
			Href:          fmt.Sprintf("/ideas/%d", i.ID),
		})
	}
	return GalleryPage{
		Page:       base,
		Ideas:      cards,
		Chips:      buildChips(base.Lang, sourceType, query),
		Query:      query,
		SourceType: sourceType,
		Count:      len(cards),
		Filtered:   sourceType != "" || query != "",
	}
}

// chipOrder, filtre chip'lerinin sabit sırası (galeriye giren tüm türler).
var chipOrder = []string{"", "pain_point", "market_derived", "ai_blended"}

func buildChips(lang, active, query string) []FilterChip {
	chips := make([]FilterChip, 0, len(chipOrder))
	for _, st := range chipOrder {
		label := translate(lang, "gallery.filter.all")
		kind := "all"
		if st != "" {
			label = translate(lang, "source_type."+st)
			kind = st
		}
		v := url.Values{}
		if st != "" {
			v.Set("source_type", st)
		}
		if query != "" {
			v.Set("q", query)
		}
		href := "/"
		if len(v) > 0 {
			href = "/?" + v.Encode()
		}
		chips = append(chips, FilterChip{Label: label, Href: href, Kind: kind, Active: active == st})
	}
	return chips
}

// buildIdea, tek kartı, kaynaklarını ve sohbet panelini görünüm modeline
// çevirir. chatErr boş değilse panelde tasarlanmış hata satırı basılır.
func buildIdea(base Page, idea *store.Idea, sources []store.IdeaSource, msgs []ChatMessage, chatErr string) IdeaPage {
	links := make([]SourceLink, 0, len(sources))
	for _, s := range sources {
		l := SourceLink{
			Platform:  platformLabel(base.Lang, s.Platform),
			Community: s.Community,
			Date:      formatDate(s.CreatedAt),
		}
		// radar_seed satırlarında community teknik bir sabittir ("radar");
		// kullanıcıya bir şey söylemez, gösterilmez.
		if s.Platform == "radar_seed" {
			l.Community = ""
		}
		if u := safeURL(s.URL); u != "" {
			l.URL = u
			l.Host = hostOf(u)
		}
		links = append(links, l)
	}
	page := IdeaPage{
		Page:          base,
		ID:            idea.ID,
		Heading:       idea.Title,
		Badge:         badgeFor(base.Lang, idea.SourceType),
		Problem:       idea.ProblemStatement,
		Solution:      idea.ProposedSolution,
		TargetUser:    idea.TargetUser,
		Quotes:        idea.ExampleQuotes,
		EvidenceCount: idea.EvidenceCount,
		LocalEvidence: parseLocalEvidenceList(base.Lang, idea.LocalEvidence),
		Sources:       links,
		DomainTags:    idea.DomainTags,
		SourceTheme:   idea.SourceTheme,
		Competitors:   strings.TrimSpace(idea.KnownCompetitorsAIGuess),
		Urgency:       idea.UrgencyScore,
		Monetization:  idea.MonetizationSignal,
		CreatedAt:     formatDate(idea.CreatedAt),
	}
	page.HasMeta = page.Urgency > 0 || page.Monetization > 0 ||
		page.SourceTheme != "" || page.Competitors != ""
	if idea.ParentIdeaID != nil && *idea.ParentIdeaID > 0 {
		page.ParentHref = fmt.Sprintf("/ideas/%d", *idea.ParentIdeaID)
	}
	page.Mine = idea.Mine
	page.Chat = buildChatPanel(base.Lang, idea.ID, idea.Title, msgs, chatErr)
	return page
}
