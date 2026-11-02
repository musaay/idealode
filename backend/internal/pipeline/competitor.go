package pipeline

// Rakip kontrolü merceği (#212 adım 3): kartın "boş kesim" iddiası LLM
// hafızasından uydurulabiliyor (kart 165-168). Yayına aday her kart için
// internetten (browser_search) gerçek rakip aranır; bulunan rakipler KODLA
// doğrulanır (link açılıyor + sayfada adı geçiyor) ve kartın mercek kaydına
// yazılır. Bu mercek BAYRAKTIR — kartı ASLA bloklamaz, hata/atlamada da
// kart yazılır (fail-open); blok kararı ölçüm sonrası ayrı iştir.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

const (
	competitorLensName    = "rakip-kontrolü"
	competitorLensVersion = "v2"

	// competitorMaxVerify, tek kartta doğrulanacak en fazla rakip sayısı
	// (model uzun liste uydurursa süre/istek patlamasın).
	competitorMaxVerify = 8
	competitorMaxBody   = 512 << 10
	competitorMaxReason = 1000

	// competitorDefaultSearches: kart başına varsayılan arama sayısı (v2 oylaması).
	competitorDefaultSearches = 3
)

// competitorSystemPrompt, rakip kontrolü sistem istemi (spec #212 adım 3, birebir).
const competitorSystemPrompt = `You verify ONE factual claim on a software product idea card: that the specific slice it targets is still open. The card may be written in Turkish — translate it to English yourself and search the web globally in English.

Step 1. Identify the card's SLICE: the specific buyer segment, workflow step or weakness it claims existing products leave open (not the broad market).
Step 2. Search the web for products that ALREADY serve exactly that slice today (live product, not a blog idea or a discontinued tool). A crowded broad market is NOT evidence — only products serving this exact slice count. Do not count products the card itself names as leaving the slice open, unless your search shows they in fact serve it.
Step 3. For every product you list, give its own official website or app-store URL that you actually opened — never guess a URL.

Return ONLY a JSON object, no prose, no citation markers:
{"slice":"one English sentence","competitors":[{"name":"...","url":"https://...","overlap":"one line"}],"verdict":"GAP_OPEN|GAP_TAKEN"}`

func competitorUserPrompt(idea *store.Idea) string {
	return fmt.Sprintf("Title: %s\nProblem: %s\nSolution: %s\nTarget user: %s\nCompetitors named on the card (AI guess): %s",
		idea.Title, idea.ProblemStatement, idea.ProposedSolution, idea.TargetUser, idea.KnownCompetitorsAIGuess)
}

type competitorEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Overlap string `json:"overlap"`
}

type competitorAnswer struct {
	Slice       string            `json:"slice"`
	Competitors []competitorEntry `json:"competitors"`
	Verdict     string            `json:"verdict"`
}

// parseCompetitorAnswer, cevabı savunmacı ayrıştırır: ilk '{' ile son '}'
// arası JSON alınır; ayrıştırılamazsa Verdict "error" olur.
func parseCompetitorAnswer(raw string) competitorAnswer {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return competitorAnswer{Verdict: "error"}
	}
	var a competitorAnswer
	if err := json.Unmarshal([]byte(raw[start:end+1]), &a); err != nil {
		return competitorAnswer{Verdict: "error"}
	}
	// Geçerli ama anlamsız JSON ({} ya da {"foo":1}): slice boş ya da
	// competitors alanı hiç yoksa (boş dizi GEÇERLİ) -> error.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw[start:end+1]), &fields)
	if _, has := fields["competitors"]; !has || strings.TrimSpace(a.Slice) == "" {
		return competitorAnswer{Verdict: "error"}
	}
	return a
}

// ---- Doğrulama (SSRF korumalı) ----

// competitorVerifier, rakip linklerini açan bileşendir; testte httptest
// istemcisi ve host denetimi enjekte edilir (paket değişkeni YOK).
type competitorVerifier struct {
	Client *http.Client
	// CheckHost, bağlanmadan önce host'u denetler (nil değilse hata = reddet).
	// Yönlendirmelerde de aynı denetim çalışır.
	CheckHost func(ctx context.Context, host string) error
}

var errBlockedAddr = errors.New("özel/yasaklı adres")

// isBlockedIP: özel, loopback, link-local, CGNAT, unspecified adresler.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xC0 == 64 { // 100.64.0.0/10
		return true
	}
	return false
}

// checkPublicHost, host'un IP literal olmadığını ve DNS çözümünün hiçbir
// özel adrese gitmediğini doğrular. lookup nil ise net.DefaultResolver.
func checkPublicHost(ctx context.Context, host string, lookup func(ctx context.Context, host string) ([]net.IP, error)) error {
	if host == "" {
		return errBlockedAddr
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return fmt.Errorf("IP literal host reddedildi: %w", errBlockedAddr)
	}
	if lookup == nil {
		lookup = func(ctx context.Context, h string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, h)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, nil
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errBlockedAddr
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%s -> %s: %w", host, ip, errBlockedAddr)
		}
	}
	return nil
}

// newCompetitorVerifier üretim doğrulayıcısını kurar: DNS ön denetimi +
// bağlantı anında (Dialer.Control) ikinci denetim (DNS rebinding'e karşı),
// en fazla 3 yönlendirme, 10 sn zaman aşımı.
func newCompetitorVerifier() competitorVerifier {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if isBlockedIP(net.ParseIP(host)) {
				return errBlockedAddr
			}
			return nil
		},
	}
	return competitorVerifier{
		Client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil},
		},
		CheckHost: func(ctx context.Context, host string) error { return checkPublicHost(ctx, host, nil) },
	}
}

// competitorNameToken: adın ilk >=3 harfli kelimesi (küçük harf).
func competitorNameToken(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) })
	for _, w := range words {
		if len([]rune(w)) >= 3 {
			return w
		}
	}
	return ""
}

// verifyCompetitor: https + özel olmayan host + 2xx + gövdede rakip adı.
func verifyCompetitor(ctx context.Context, v competitorVerifier, c competitorEntry) bool {
	token := competitorNameToken(c.Name)
	if token == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	check := func(host string) error {
		if v.CheckHost == nil {
			return nil
		}
		return v.CheckHost(ctx, host)
	}
	if check(u.Hostname()) != nil {
		return false
	}
	client := *v.Client // CheckRedirect yalnız bu çağrıya özel
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("çok fazla yönlendirme")
		}
		if req.URL.Scheme != "https" {
			return errors.New("https dışı yönlendirme")
		}
		return check(req.URL.Hostname())
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; IdeaLodeBot/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, competitorMaxBody))
	if err != nil && len(body) == 0 {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), token)
}

// ---- Mercek ----

// competitorOpts, runCompetitorCheckWith'in enjekte edilebilir parçaları.
type competitorOpts struct {
	System   string // boşsa competitorSystemPrompt (lens-ab --prompt-file)
	Verifier *competitorVerifier
	Timeout  time.Duration // HER arama çağrısının zaman aşımı; 0 -> 3 dk
	Searches int           // kart başına arama sayısı; <1 -> 3, >5 -> 5
}

// runCompetitorCheck, kart için rakip kontrolünü çalıştırır. Asla kartı
// bloklamaz; hata/atlamada "error"/"skipped" verdict'i döner.
func runCompetitorCheck(ctx context.Context, chat llm.Chat, idea *store.Idea) store.LensVerdict {
	return runCompetitorCheckWith(ctx, chat, idea, competitorOpts{})
}

func runCompetitorCheckWith(ctx context.Context, chat llm.Chat, idea *store.Idea, opts competitorOpts) store.LensVerdict {
	v, _, _ := runCompetitorCheckCounts(ctx, chat, idea, opts)
	return v
}

// runCompetitorCheckCounts (v2): kart başına opts.Searches sıralı arama, adaylar
// ad bazında birleşir, birleşikten en çok competitorMaxVerify rakip doğrulanır;
// doğrulanmış >=2 -> fail. Hepsi hata -> error. verdict'e ek olarak doğrulanmış/doğrulanamayan
// rakip sayılarını da döner (lens-ab CSV'si için).
func runCompetitorCheckCounts(ctx context.Context, chat llm.Chat, idea *store.Idea, opts competitorOpts) (store.LensVerdict, int, int) {
	v := store.LensVerdict{
		Lens:          competitorLensName,
		PromptVersion: competitorLensVersion,
		Subject:       "card",
		At:            time.Now().UTC(),
	}
	if named, ok := chat.(llm.NamedChat); ok {
		v.Model = named.ModelName()
	}
	sc, ok := chat.(llm.SearchChat)
	if !ok {
		v.Verdict, v.Reason = "skipped", "istemci arama desteklemiyor"
		return v, 0, 0
	}
	system := opts.System
	if system == "" {
		system = competitorSystemPrompt
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	searches := opts.Searches
	if searches < 1 {
		searches = competitorDefaultSearches
	}
	if searches > 5 {
		searches = 5
	}

	// Aramalar SIRALI (Groq TPM sınırı); her biri kendi zaman aşımıyla.
	// Adaylar ad bazında (büyük/küçük harf duyarsız) birleştirilir.
	var cands []competitorEntry
	candSeen := map[string]bool{}
	slice, lastErr := "", ""
	okSearches := 0
	for i := 0; i < searches; i++ {
		if ctx.Err() != nil {
			lastErr = "bağlam iptal: " + ctx.Err().Error()
			break
		}
		callCtx, cancel := context.WithTimeout(llm.WithStage(ctx, "competitor"), timeout)
		raw, err := sc.ChatWithBrowserSearch(callCtx, system, competitorUserPrompt(idea))
		cancel()
		if err != nil {
			lastErr = "LLM hatası: " + err.Error()
			log.Printf("competitor: %q arama %d/%d hata: %v", idea.Title, i+1, searches, err)
			continue
		}
		ans := parseCompetitorAnswer(raw)
		if ans.Verdict == "error" {
			lastErr = fmt.Sprintf("LLM yanıtı ayrıştırılamadı: %.120s", raw)
			log.Printf("competitor: %q arama %d/%d yanıt ayrıştırılamadı", idea.Title, i+1, searches)
			continue
		}
		okSearches++
		if slice == "" {
			slice = strings.TrimSpace(ans.Slice)
		}
		log.Printf("competitor: %q arama %d/%d LLM verdict=%s, %d aday rakip", idea.Title, i+1, searches, ans.Verdict, len(ans.Competitors))
		for _, c := range ans.Competitors {
			key := strings.ToLower(strings.TrimSpace(c.Name))
			if key == "" || candSeen[key] {
				continue
			}
			candSeen[key] = true
			cands = append(cands, c)
		}
	}
	if okSearches == 0 {
		v.Verdict, v.Reason = "error", truncateRunes(fmt.Sprintf("aramalar: 0/%d başarılı | %s", searches, lastErr), 300)
		return v, 0, 0
	}

	verifier := newCompetitorVerifier()
	if opts.Verifier != nil {
		verifier = *opts.Verifier
	}
	var okList, badList []string
	seenURL := map[string]bool{}
	for n, c := range cands {
		if n >= competitorMaxVerify {
			break
		}
		name := strings.TrimSpace(c.Name)
		u := strings.ToLower(strings.TrimSpace(c.URL))
		if u != "" && seenURL[u] { // aynı URL iki kez doğrulanmaz
			continue
		}
		seenURL[u] = true
		if verifyCompetitor(ctx, verifier, c) {
			okList = append(okList, fmt.Sprintf("%s (%s)", name, strings.TrimSpace(c.URL)))
		} else {
			badList = append(badList, name)
		}
	}
	if len(okList) >= 2 {
		v.Verdict = "fail" // boşluk dolu — BAYRAK, bloklamaz
	} else {
		v.Verdict = "pass"
	}
	join := func(l []string, sep string) string {
		if len(l) == 0 {
			return "-"
		}
		return strings.Join(l, sep)
	}
	v.Reason = truncateRunes(fmt.Sprintf("slice: %s | doğrulanmış: %s | doğrulanamayan: %s | aramalar: %d/%d başarılı",
		slice, join(okList, "; "), join(badList, ", "), okSearches, searches), competitorMaxReason)
	return v, len(okList), len(badList)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---- Boru hattı bağlantısı ----

type competitorChatKey struct{}

// WithCompetitorChat, rakip kontrolü için AYRI (arama destekli) istemciyi
// ctx'e iliştirir (ProcessSeeds/SynthesizeIdeas imzaları değişmesin diye).
// c nil ise ctx değişmez.
func WithCompetitorChat(ctx context.Context, c llm.Chat) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, competitorChatKey{}, c)
}

// applyCompetitorFlag, cfg.CompetitorCheck açıksa kartın LensVerdicts'ine
// rakip-kontrolü kaydını ekler. Dedup'tan SONRA, InsertIdea'dan HEMEN ÖNCE
// çağrılır. Bloklama YOK, eliminations'a yazma YOK; kapalıyken hiçbir şey
// yapmaz (arama çağrısı 0).
func applyCompetitorFlag(ctx context.Context, cfg *config.Config, chat llm.Chat, idea *store.Idea) {
	if cfg == nil || !cfg.CompetitorCheck {
		return
	}
	cc := chat
	if c, ok := ctx.Value(competitorChatKey{}).(llm.Chat); ok && c != nil {
		cc = c
	}
	v := runCompetitorCheckWith(ctx, cc, idea, competitorOpts{Searches: cfg.CompetitorSearches})
	idea.LensVerdicts = append(idea.LensVerdicts, v)
	log.Printf("competitor: %q -> %s: %s", idea.Title, v.Verdict, v.Reason)
}
