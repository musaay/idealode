package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// fakeStore, canlı DB olmadan handler testleri için IdeaStore uygular.
type fakeStore struct {
	ideas   []store.Idea
	sources []store.IdeaSource
	err     error

	lastFilter store.IdeaFilter
}

func (f *fakeStore) ListIdeasFiltered(ctx context.Context, flt store.IdeaFilter) ([]store.Idea, error) {
	f.lastFilter = flt
	if f.err != nil {
		return nil, f.err
	}
	out := []store.Idea{}
	for _, i := range f.ideas {
		if flt.SourceType != "" && i.SourceType != flt.SourceType {
			continue
		}
		if q := strings.ToLower(flt.Query); q != "" &&
			!strings.Contains(strings.ToLower(i.Title), q) &&
			!strings.Contains(strings.ToLower(i.ProblemStatement), q) {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

func (f *fakeStore) GetIdea(ctx context.Context, id int64) (*store.Idea, error) {
	if f.err != nil {
		return nil, f.err
	}
	for i := range f.ideas {
		if f.ideas[i].ID == id {
			cp := f.ideas[i]
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sources, nil
}

// evilQuote, hem birebirlik hem XSS kaçışı testinde kullanılır.
const evilQuote = `He said "I'd pay $10/mo" <script>alert(1)</script> & meant it`

func sampleStore() *fakeStore {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	return &fakeStore{
		ideas: []store.Idea{
			{
				ID:               1,
				Title:            "Randevu hatırlatma botu",
				ProblemStatement: "Küçük işletmeler randevu takibini elle yapıyor.",
				ProposedSolution: "WhatsApp üzerinden otomatik hatırlatma.",
				TargetUser:       "KOBİ sahipleri",
				EvidenceCount:    4,
				ExampleQuotes:    []string{evilQuote, "Second verbatim quote."},
				SourceType:       "pain_point",
				DomainTags:       []string{"scheduling", "smb"},
				LocalEvidence:    []string{`Uygulama sürekli bildirim atmıyor — [googleplay] https://play.google.com/store/apps/details?id=x`},
				UrgencyScore:     4,
				SourceTheme:      "scheduling",
				CreatedAt:        base,
			},
			{
				ID:               2,
				Title:            "Konuşma terapisi takip aracı",
				ProblemStatement: "Terapistler seans notlarını dağınık tutuyor.",
				ProposedSolution: "Tek panelde seans takibi.",
				EvidenceCount:    1,
				ExampleQuotes:    []string{"Kanıt (SpeechPal): $12k MRR — https://example.com/seed"},
				SourceType:       "market_derived",
				DomainTags:       []string{"healthcare"},
				LocalEvidence:    []string{},
				CreatedAt:        base.Add(-48 * time.Hour),
			},
		},
		sources: []store.IdeaSource{
			{Platform: "hackernews", Community: "ask", URL: "https://news.ycombinator.com/item?id=1", CreatedAt: base},
			{Platform: "radar_seed", Community: "radar", URL: "javascript:alert(1)", CreatedAt: base},
		},
	}
}

func newTestServer(t *testing.T, fs *fakeStore) http.Handler {
	t.Helper()
	return NewServer(fs).Handler()
}

func do(t *testing.T, h http.Handler, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --------------------------------------------------------------- şablonlar

func TestTemplatesParse(t *testing.T) {
	tpls := mustParseTemplates()
	if len(tpls) != len(pageTemplates) {
		t.Fatalf("şablon sayısı: %d, beklenen %d", len(tpls), len(pageTemplates))
	}
	for name, tpl := range tpls {
		if tpl.Lookup("content") == nil {
			t.Errorf("%q şablonunda \"content\" bloğu yok", name)
		}
	}
}

// --------------------------------------------------------------------- i18n

func TestCatalogKeysMatch(t *testing.T) {
	cats := Catalogs()
	if len(cats) < 2 {
		t.Fatalf("en az iki katalog beklenir, %d bulundu", len(cats))
	}
	ref := cats[DefaultLang]
	if len(ref) == 0 {
		t.Fatalf("%q kataloğu boş", DefaultLang)
	}
	for lang, c := range cats {
		for k := range ref {
			if _, ok := c[k]; !ok {
				t.Errorf("%q kataloğunda eksik anahtar: %s", lang, k)
			}
		}
		for k := range c {
			if _, ok := ref[k]; !ok {
				t.Errorf("%q kataloğunda fazladan anahtar: %s", lang, k)
			}
		}
	}
}

func TestCatalogValuesNonEmpty(t *testing.T) {
	for lang, c := range Catalogs() {
		for k, v := range c {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s/%s boş metin", lang, k)
			}
		}
	}
}

func TestNegotiateLang(t *testing.T) {
	cases := []struct{ header, want string }{
		{"", ""},
		{"tr-TR,tr;q=0.9,en;q=0.8", "tr"},
		{"en-US,en;q=0.9", "en"},
		{"de,fr;q=0.9", ""},
		{"de,en;q=0.4,tr;q=0.9", "tr"},
		{"*", ""},
	}
	for _, c := range cases {
		if got := negotiateLang(c.header); got != c.want {
			t.Errorf("negotiateLang(%q) = %q, beklenen %q", c.header, got, c.want)
		}
	}
}

func TestTranslateFallsBackToKey(t *testing.T) {
	if got := translate("tr", "yok.olan.anahtar"); got != "yok.olan.anahtar" {
		t.Errorf("eksik anahtar için %q döndü", got)
	}
	if got := translate("tr", "gallery.count", 3); got != "3 fikir" {
		t.Errorf("gallery.count = %q", got)
	}
	if got := translate("en", "gallery.count", 3); got != "3 ideas" {
		t.Errorf("gallery.count(en) = %q", got)
	}
}

// ------------------------------------------------- local_evidence parser'ı

func TestParseLocalEvidence(t *testing.T) {
	cases := []struct {
		name string
		line string
		want EvidenceItem
	}{
		{
			name: "tam satır",
			line: `Bildirimler hiç gelmiyor — [googleplay] https://play.google.com/store/apps/details?id=x`,
			want: EvidenceItem{
				Quote:    "Bildirimler hiç gelmiyor",
				Platform: "googleplay",
				URL:      "https://play.google.com/store/apps/details?id=x",
				Host:     "play.google.com",
			},
		},
		{
			name: "alıntının içinde de ayraç var",
			line: `A — [x] hakkında: yavaş — [hackernews] https://news.ycombinator.com/item?id=9`,
			want: EvidenceItem{
				Quote:    `A — [x] hakkında: yavaş`,
				Platform: "hackernews",
				URL:      "https://news.ycombinator.com/item?id=9",
				Host:     "news.ycombinator.com",
			},
		},
		{
			name: "URL yok",
			line: `Sadece alıntı — [technopat] `,
			want: EvidenceItem{Quote: "Sadece alıntı", Platform: "technopat"},
		},
		{
			name: "ayrıştırılamaz satır olduğu gibi kalır",
			line: `Düz bir alıntı, ayraçsız`,
			want: EvidenceItem{Quote: "Düz bir alıntı, ayraçsız"},
		},
		{
			name: "güvensiz şema düşer, alıntı kalır",
			line: `Kötü link — [x] javascript:alert(1)`,
			want: EvidenceItem{Quote: "Kötü link", Platform: "x"},
		},
		{
			name: "boş satır",
			line: "   ",
			want: EvidenceItem{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseLocalEvidence(c.line)
			if got != c.want {
				t.Errorf("parseLocalEvidence(%q)\n  got  %+v\n  want %+v", c.line, got, c.want)
			}
		})
	}
}

func TestParseLocalEvidenceListSkipsEmpty(t *testing.T) {
	got := parseLocalEvidenceList("tr", []string{"", "  ", "gerçek alıntı"})
	if len(got) != 1 || got[0].Quote != "gerçek alıntı" {
		t.Fatalf("beklenmeyen sonuç: %+v", got)
	}
}

func TestSafeURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a": "https://example.com/a",
		"http://example.com":    "http://example.com",
		"javascript:alert(1)":   "",
		"data:text/html,x":      "",
		"/relative":             "",
		"":                      "",
	}
	for in, want := range cases {
		if got := safeURL(in); got != want {
			t.Errorf("safeURL(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// ----------------------------------------------------------------- healthz

func TestHealthz(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum = %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"ok"}` {
		t.Errorf("gövde = %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// ------------------------------------------------------------------ galeri

func TestGalleryListsIdeas(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Randevu hatırlatma botu",
		"Konuşma terapisi takip aracı",
		`href="/ideas/1"`,
		`href="/ideas/2"`,
		"Ağrı Noktası",
		"Pazar Verisi",
		"2 fikir",
		"#scheduling",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("galeri gövdesinde %q yok", want)
		}
	}
}

func TestGalleryFilterAndSearch(t *testing.T) {
	fs := sampleStore()
	h := newTestServer(t, fs)

	rec := do(t, h, http.MethodGet, "/?source_type=market_derived")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum = %d", rec.Code)
	}
	if fs.lastFilter.SourceType != "market_derived" {
		t.Errorf("filtre store'a geçmedi: %+v", fs.lastFilter)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Randevu hatırlatma botu") {
		t.Error("filtre dışı kart listelendi")
	}
	if !strings.Contains(body, `aria-current="page"`) {
		t.Error("aktif chip aria-current taşımıyor")
	}

	rec = do(t, h, http.MethodGet, "/?q=terapi")
	if fs.lastFilter.Query != "terapi" {
		t.Errorf("arama store'a geçmedi: %+v", fs.lastFilter)
	}
	if !strings.Contains(rec.Body.String(), "1 fikir") {
		t.Error("arama sonucu sayısı yanlış")
	}

	// Bilinmeyen source_type sessizce yok sayılır (ham değer sorguya gitmez).
	do(t, h, http.MethodGet, "/?source_type=drop%20table")
	if fs.lastFilter.SourceType != "" {
		t.Errorf("bilinmeyen tür sorguya sızdı: %q", fs.lastFilter.SourceType)
	}
}

func TestGalleryEmptyState(t *testing.T) {
	rec := do(t, newTestServer(t, &fakeStore{}), http.MethodGet, "/?q=yok")
	if rec.Code != http.StatusOK {
		t.Fatalf("boş liste durumu = %d, 200 bekleniyor", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Eşleşen fikir yok") {
		t.Error("boş durum metni yok")
	}
	if !strings.Contains(body, "Filtreleri temizle") {
		t.Error("filtre uygulanmışken temizleme bağlantısı yok")
	}
}

// ------------------------------------------------------------ kart detayı

func TestIdeaDetail(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Randevu hatırlatma botu",
		"Küçük işletmeler randevu takibini elle yapıyor.",
		"WhatsApp üzerinden otomatik hatırlatma.",
		"KOBİ sahipleri",
		"Uygulama sürekli bildirim atmıyor",
		"news.ycombinator.com",
		`rel="noopener noreferrer"`,
		`target="_blank"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detay gövdesinde %q yok", want)
		}
	}
	if strings.Contains(body, "javascript:alert") {
		t.Error("güvensiz kaynak URL'si sayfaya basıldı")
	}
}

func TestIdeaQuotesAreVerbatimAndEscaped(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/1")
	body := rec.Body.String()

	// Alıntı DB'deki metinle birebir aynıdır; HTML'de yalnız kaçışlanır.
	escaped := template.HTMLEscapeString(evilQuote)
	if !strings.Contains(body, escaped) {
		t.Fatalf("alıntı birebir bulunamadı.\nbeklenen: %s", escaped)
	}
	// Kaçış gerçekten işini görüyor: çalıştırılabilir script yok.
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("alıntıdaki script etiketi kaçışlanmadan basıldı")
	}
	if !strings.Contains(body, "Second verbatim quote.") {
		t.Error("ikinci alıntı düştü")
	}
}

func TestIdeaLocalEvidenceEmptyState(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/2")
	if rec.Code != http.StatusOK {
		t.Fatalf("durum = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "henüz yerel talep kanıtı eşleşmedi") {
		t.Error("yerel talep boş durum metni yok")
	}
}

func TestIdeaNotFound(t *testing.T) {
	h := newTestServer(t, sampleStore())
	for _, target := range []string{"/ideas/999", "/ideas/abc", "/ideas/-1", "/ideas/", "/bilinmeyen"} {
		rec := do(t, h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s durum = %d, 404 bekleniyor", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Sayfa bulunamadı") {
			t.Errorf("%s için 404 şablonu render edilmedi", target)
		}
	}
}

// TestAPIErrorRendersGatewayPage — API'ye ulaşılamadığında (ErrNotFound
// DIŞI her hata) her sayfa 502 varyantını gösterir, süreç düşmez.
func TestAPIErrorRendersGatewayPage(t *testing.T) {
	for _, target := range []string{"/", "/?source_type=pain_point&q=bot", "/ideas/1"} {
		fs := sampleStore()
		fs.err = context.DeadlineExceeded
		rec := do(t, newTestServer(t, fs), http.MethodGet, target)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s durum = %d, 502 bekleniyor", target, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Servis şu an yanıt vermiyor") {
			t.Errorf("%s için 502 başlığı render edilmedi", target)
		}
		if !strings.Contains(body, `data-status="502"`) {
			t.Errorf("%s için 502 varyant işareti yok", target)
		}
		if !strings.Contains(body, "geçici bir bağlantı kesintisi") {
			t.Errorf("%s için 502 ipucu satırı yok", target)
		}
		if strings.Contains(body, "context deadline exceeded") {
			t.Errorf("%s: ham hata metni sayfaya sızdı", target)
		}
	}
}

// TestAPIErrorGatewayPageEN — 502 metni iki dilde de çevrilidir.
func TestAPIErrorGatewayPageEN(t *testing.T) {
	fs := sampleStore()
	fs.err = context.DeadlineExceeded
	rec := do(t, newTestServer(t, fs), http.MethodGet, "/?lang=en")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("durum = %d, 502 bekleniyor", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not responding right now") {
		t.Error("502 sayfası İngilizce render edilmedi")
	}
}

// TestWrappedNotFoundStillRenders404 — istemci ErrNotFound'u sararak döner;
// bu 502 değil 404 olmalı.
func TestWrappedNotFoundStillRenders404(t *testing.T) {
	fs := sampleStore()
	fs.err = fmt.Errorf("api (/api/ideas/1): %w", store.ErrNotFound)
	rec := do(t, newTestServer(t, fs), http.MethodGet, "/ideas/1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("durum = %d, 404 bekleniyor", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sayfa bulunamadı") {
		t.Error("404 şablonu render edilmedi")
	}
}

// panicStore, recover yolunu (500 sayfası) sürdürmek için panikleyen store.
type panicStore struct{ fakeStore }

func (p *panicStore) ListIdeasFiltered(ctx context.Context, f store.IdeaFilter) ([]store.Idea, error) {
	panic("beklenmeyen durum")
}

// TestPanicRenders500 — 500 sayfası hâlâ recover yolunda kullanılır.
func TestPanicRenders500(t *testing.T) {
	rec := do(t, NewServer(&panicStore{}).Handler(), http.MethodGet, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("durum = %d, 500 bekleniyor", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ters gitti") {
		t.Error("500 şablonu render edilmedi")
	}
}

// ---------------------------------------------------------------- dil/tema

func TestLanguageSwitch(t *testing.T) {
	h := newTestServer(t, sampleStore())

	rec := do(t, h, http.MethodGet, "/?lang=en")
	body := rec.Body.String()
	if !strings.Contains(body, `<html lang="en"`) {
		t.Error("lang attribute en değil")
	}
	if !strings.Contains(body, "Idea Pool") || !strings.Contains(body, "Pain Point") {
		t.Error("EN arayüz metinleri yok")
	}
	// Kart içeriği ASLA çevrilmez.
	if !strings.Contains(body, "Randevu hatırlatma botu") {
		t.Error("kart başlığı EN'de değişti")
	}
	var langCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieLang {
			langCookie = c
		}
	}
	if langCookie == nil || langCookie.Value != "en" {
		t.Fatalf("lang cookie yazılmadı: %+v", langCookie)
	}
	if langCookie.MaxAge <= 0 {
		t.Error("lang cookie kalıcı değil")
	}

	// Cookie ile ?lang= olmadan da EN gelir.
	rec = do(t, h, http.MethodGet, "/", langCookie)
	if !strings.Contains(rec.Body.String(), "Idea Pool") {
		t.Error("cookie'den dil okunmadı")
	}

	// Varsayılan TR.
	rec = do(t, h, http.MethodGet, "/")
	if !strings.Contains(rec.Body.String(), `<html lang="tr"`) {
		t.Error("varsayılan dil TR değil")
	}
}

func TestAcceptLanguageFallback(t *testing.T) {
	h := newTestServer(t, sampleStore())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `<html lang="en"`) {
		t.Error("Accept-Language dikkate alınmadı")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("Accept-Language için cookie yazılmamalı")
	}
}

func TestThemeSwitch(t *testing.T) {
	h := newTestServer(t, sampleStore())

	// Varsayılan: data-theme boş -> prefers-color-scheme geçerli.
	rec := do(t, h, http.MethodGet, "/")
	if !strings.Contains(rec.Body.String(), `data-theme=""`) {
		t.Error("varsayılanda data-theme boş değil")
	}

	rec = do(t, h, http.MethodGet, "/?theme=dark")
	if !strings.Contains(rec.Body.String(), `data-theme="dark"`) {
		t.Error("koyu tema uygulanmadı")
	}
	var themeCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieTheme {
			themeCookie = c
		}
	}
	if themeCookie == nil || themeCookie.Value != "dark" {
		t.Fatalf("theme cookie yazılmadı: %+v", themeCookie)
	}

	rec = do(t, h, http.MethodGet, "/", themeCookie)
	if !strings.Contains(rec.Body.String(), `data-theme="dark"`) {
		t.Error("cookie'den tema okunmadı")
	}

	// Geçersiz değer yok sayılır.
	rec = do(t, h, http.MethodGet, "/?theme=neon")
	if !strings.Contains(rec.Body.String(), `data-theme=""`) {
		t.Error("geçersiz tema değeri kabul edildi")
	}
}

func TestLanguageLinksPreserveFilters(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/?source_type=pain_point&q=bot")
	body := rec.Body.String()
	if !strings.Contains(body, "lang=en") || !strings.Contains(body, "q=bot") {
		t.Error("dil bağlantısı mevcut filtreleri korumuyor")
	}
}

// -------------------------------------------------------------- güvenlik

func TestSecurityHeaders(t *testing.T) {
	rec := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/")
	want := "default-src 'self'; style-src 'self' fonts.googleapis.com; font-src fonts.gstatic.com"
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("CSP = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestNoExternalScriptsOrStyles(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/").Body.String()
	// Tek CSS + tek JS, ikisi de kendi origin'imizden.
	if strings.Count(body, "<script") != 1 {
		t.Errorf("beklenen tek <script>, bulunan %d", strings.Count(body, "<script"))
	}
	if !strings.Contains(body, `src="/static/app.js`) {
		t.Error("app.js kendi origin'imizden yüklenmiyor")
	}
	if !strings.Contains(body, `href="/static/app.css`) {
		t.Error("app.css kendi origin'imizden yüklenmiyor")
	}
	// Dışarıdan yalnız Google Fonts stili yüklenir (CSP ile uyumlu).
	for _, host := range []string{"cdn.", "unpkg.com", "jsdelivr", "cdnjs"} {
		if strings.Contains(body, host) {
			t.Errorf("sayfada dış CDN referansı var: %s", host)
		}
	}
}

func TestStaticAssetsServed(t *testing.T) {
	h := newTestServer(t, sampleStore())
	for _, p := range []string{"/static/app.css", "/static/app.js"} {
		rec := do(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK {
			t.Errorf("%s durum = %d", p, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s boş", p)
		}
	}
}

// ------------------------------------------------------------------ kabuk

func TestShellPresentOnEveryPage(t *testing.T) {
	h := newTestServer(t, sampleStore())
	for _, target := range []string{"/", "/ideas/1", "/ideas/999"} {
		body := do(t, h, http.MethodGet, target).Body.String()
		for _, want := range []string{
			`class="sidenav"`,       // masaüstü sol nav
			`class="mobile-header"`, // mobil üst header
			`class="mobile-nav"`,    // mobil alt nav
			`class="topbar"`,        // masaüstü breadcrumb çubuğu
			`id="main"`,
			`class="skip-link"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s sayfasında %q yok", target, want)
			}
		}
	}
}

func TestGalleryShellState(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `class="navlink-count">2<`) {
		t.Error("sol nav kart sayacı yok")
	}
	// Galeride geri oku ve breadcrumb kartı olmaz.
	if strings.Contains(body, "crumb-current") {
		t.Error("galeride breadcrumb kart adı gösterildi")
	}
}

func TestIdeaShellState(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/1").Body.String()
	if !strings.Contains(body, "crumb-current") {
		t.Error("detayda breadcrumb kart adı yok")
	}
	if !strings.Contains(body, "Randevu hatırlatma botu") {
		t.Error("mobil başlık/breadcrumb kart adını taşımıyor")
	}
	// Kart sayısı detay sayfasında bilinmiyor: uydurma sayı basılmaz.
	if strings.Contains(body, "navlink-count") {
		t.Error("detayda bilinmeyen kart sayısı basıldı")
	}
}

func TestBothThemeTogglesShareState(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/?theme=dark").Body.String()
	// Sol nav bloğu + masaüstü üst çubuk pill'i + mobil header ikonu.
	if n := strings.Count(body, `data-next-theme=`); n != 3 {
		t.Fatalf("tema anahtarı sayısı = %d, 3 bekleniyor (sol nav + üst çubuk + mobil)", n)
	}
	if n := strings.Count(body, `data-next-theme="light"`); n != 3 {
		t.Errorf("anahtarlar aynı sonraki temayı göstermiyor (%d)", n)
	}
}

func TestClipTitle(t *testing.T) {
	short := "Kısa başlık"
	if got := clipTitle(short); got != short {
		t.Errorf("kısa başlık değişti: %q", got)
	}
	long := strings.Repeat("ğ", 40)
	got := clipTitle(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("uzun başlık kırpılmadı: %q", got)
	}
	if r := []rune(got); len(r) != breadcrumbMax+1 {
		t.Errorf("kırpma uzunluğu = %d rune", len(r))
	}
}

// ------------------------------------------------- tarih ve platform etiketi

func TestFormatDateHidesZeroTime(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"sıfır zaman", time.Time{}, ""},
		{"Go sıfırı (0001-01-01)", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), ""},
		{"1970 öncesi", time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), ""},
		{"gerçek tarih", time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC), "2026-08-21"},
	}
	for _, c := range cases {
		if got := formatDate(c.in); got != c.want {
			t.Errorf("%s: formatDate = %q, beklenen %q", c.name, got, c.want)
		}
	}
}

func TestPlatformLabel(t *testing.T) {
	cases := map[string]string{
		"radar_seed":    "Radar",
		"hackernews":    "Hacker News",
		"googleplay":    "Google Play",
		"stackexchange": "Stack Exchange",
		"producthunt":   "Product Hunt",
		"technopat":     "Technopat",
		"github":        "GitHub",
		// Eşlemesi olmayan platform HAM kalır.
		"bilinmeyen_kaynak": "bilinmeyen_kaynak",
	}
	for _, lang := range SupportedLangs() {
		for in, want := range cases {
			if got := platformLabel(lang, in); got != want {
				t.Errorf("platformLabel(%q, %q) = %q, beklenen %q", lang, in, got, want)
			}
		}
	}
}

// TestMarketDerivedSourceRow, canlı DB'de görülen iki kusuru kilitler:
// sıfır tarihin basılması ve ham "radar_seed"/"radar" değerlerinin sızması.
func TestMarketDerivedSourceRow(t *testing.T) {
	fs := sampleStore()
	fs.sources = []store.IdeaSource{
		// created_at yok: seeds.go tohumu Go sıfır zamanıyla yazar.
		{Platform: "radar_seed", Community: "radar", URL: "https://example.com/seed"},
	}
	body := do(t, newTestServer(t, fs), http.MethodGet, "/ideas/2").Body.String()

	if strings.Contains(body, "0001-01-01") {
		t.Error("sıfır tarih sayfaya basıldı")
	}
	if strings.Contains(body, "radar_seed") {
		t.Error("ham platform anahtarı sayfaya basıldı")
	}
	if strings.Contains(body, `class="source-community"`) {
		t.Error("radar_seed satırında teknik community gösterildi")
	}
	if !strings.Contains(body, ">Radar<") {
		t.Error("platform etiketi Radar olarak basılmadı")
	}
	if !strings.Contains(body, "example.com") {
		t.Error("kaynak linki düştü")
	}
}

func TestLocalEvidencePlatformLabelled(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/1").Body.String()
	if !strings.Contains(body, "Google Play") {
		t.Error("yerel talep satırında platform etiketi çevrilmedi")
	}
	// Alıntının kendisi değişmez.
	if !strings.Contains(body, "Uygulama sürekli bildirim atmıyor") {
		t.Error("yerel talep alıntısı bozuldu")
	}
}

// ------------------------------------------------------ üst çubuk kontrolleri

// Referans App.tsx üst çubuk sağı: dil anahtarı + tema düğmesi + Idea Copilot.
func TestTopbarHasControls(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/").Body.String()

	actions := strings.Index(body, `class="topbar-actions"`)
	if actions < 0 {
		t.Fatal("üst çubukta kontrol grubu yok")
	}
	topbar := strings.Index(body, `class="topbar"`)
	if topbar < 0 || topbar > actions {
		t.Error("kontrol grubu üst çubuğun içinde değil")
	}

	rest := body[actions:]
	tr := strings.Index(rest, `>TR<`)
	theme := strings.Index(rest, `id="theme-toggle-topbar"`)
	copilot := strings.Index(rest, `class="copilot-button"`)
	if tr < 0 || theme < 0 || copilot < 0 {
		t.Fatalf("eksik kontrol: tr=%d theme=%d copilot=%d", tr, theme, copilot)
	}
	// Referanstaki sıra: dil, tema, Idea Copilot.
	if !(tr < theme && theme < copilot) {
		t.Errorf("kontrol sırası referanstan farklı: tr=%d theme=%d copilot=%d", tr, theme, copilot)
	}

	// Tema etiketi AKTİF temayı gösterir; varsayılan açık tema.
	if !strings.Contains(rest, "Aydınlık Mod") {
		t.Error("tema düğmesi aktif tema etiketini göstermiyor")
	}
	// Sohbet henüz yok: düğme gezinmez.
	if !strings.Contains(rest, `aria-disabled="true"`) {
		t.Error("Idea Copilot düğmesi pasif olarak işaretlenmemiş")
	}
	if strings.Contains(rest[:copilot+400], `href="#"`) {
		t.Error("Idea Copilot boş bağlantı olarak render edildi")
	}
	if !strings.Contains(rest, "Idea Copilot") {
		t.Error("Idea Copilot etiketi yok")
	}
}

func TestTopbarControlsEN(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/ideas/1?lang=en").Body.String()
	rest := body[strings.Index(body, `class="topbar-actions"`):]

	if !strings.Contains(rest, "Light Mode") {
		t.Error("EN tema etiketi yok")
	}
	if !strings.Contains(rest, `title="Coming soon"`) {
		t.Error("EN Idea Copilot ipucu yok")
	}
}

// Tema bağlantıları JS'siz de çalışır: her anahtar gerçek ?theme= adresidir.
func TestThemeLinksWorkWithoutJS(t *testing.T) {
	h := newTestServer(t, sampleStore())
	body := do(t, h, http.MethodGet, "/").Body.String()
	if strings.Count(body, `href="/?theme=dark"`) < 2 {
		t.Error("tema anahtarları gerçek bağlantı değil")
	}

	rec := do(t, h, http.MethodGet, "/?theme=dark")
	if !strings.Contains(rec.Body.String(), `data-theme="dark"`) {
		t.Error("?theme=dark uygulanmadı")
	}
	if !strings.Contains(rec.Body.String(), "Karanlık Mod") {
		t.Error("koyu temada üst çubuk etiketi güncellenmedi")
	}
}

// Referans MobileBottomNav iki sekmelidir: Galeri + Idea Copilot.
func TestMobileBottomNavHasTwoTabs(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/").Body.String()

	start := strings.Index(body, `class="mobile-nav"`)
	if start < 0 {
		t.Fatal("mobil alt nav yok")
	}
	nav := body[start:]
	nav = nav[:strings.Index(nav, "</nav>")]

	if n := strings.Count(nav, "mobile-nav-item"); n != 2 {
		t.Fatalf("mobil alt nav sekme sayısı = %d, 2 bekleniyor", n)
	}
	if !strings.Contains(nav, "Galeri") || !strings.Contains(nav, "Idea Copilot") {
		t.Error("sekme etiketleri referanstaki gibi değil")
	}
	// Galeri önce gelir.
	if strings.Index(nav, "Galeri") > strings.Index(nav, "Idea Copilot") {
		t.Error("sekme sırası referanstan farklı")
	}
	// Sohbet henüz yok: ikinci sekme gezinmez.
	if !strings.Contains(nav, `aria-disabled="true"`) {
		t.Error("Idea Copilot sekmesi pasif işaretlenmemiş")
	}
	if strings.Contains(nav, `href="#"`) {
		t.Error("Idea Copilot sekmesi boş bağlantı olarak render edildi")
	}
}

func TestMobileBottomNavCopilotEN(t *testing.T) {
	body := do(t, newTestServer(t, sampleStore()), http.MethodGet, "/?lang=en").Body.String()
	nav := body[strings.Index(body, `class="mobile-nav"`):]
	nav = nav[:strings.Index(nav, "</nav>")]

	if !strings.Contains(nav, `title="Coming soon"`) {
		t.Error("EN ipucu yok")
	}
	if !strings.Contains(nav, "Gallery") {
		t.Error("EN galeri etiketi yok")
	}
}
