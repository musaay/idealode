package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

//go:embed i18n/*.json
var i18nFS embed.FS

// DefaultLang, hiçbir ipucu yoksa kullanılan dil (Rev 2: TR).
const DefaultLang = "tr"

// supportedLangs, katalog dosyası bulunan diller (sıra = tercih sırası).
var supportedLangs = []string{"tr", "en"}

// Catalog, tek bir dilin anahtar -> metin eşlemesi.
type Catalog map[string]string

// catalogs, başlangıçta bir kez yüklenir (istek başına dosya okuma yok).
var catalogs = mustLoadCatalogs()

func mustLoadCatalogs() map[string]Catalog {
	out := make(map[string]Catalog, len(supportedLangs))
	for _, lang := range supportedLangs {
		b, err := i18nFS.ReadFile("i18n/" + lang + ".json")
		if err != nil {
			panic(fmt.Sprintf("i18n kataloğu okunamadı (%s): %v", lang, err))
		}
		var c Catalog
		if err := json.Unmarshal(b, &c); err != nil {
			panic(fmt.Sprintf("i18n kataloğu bozuk (%s): %v", lang, err))
		}
		out[lang] = c
	}
	return out
}

// Catalogs, yüklü katalogları döner (test ve doğrulama için).
func Catalogs() map[string]Catalog { return catalogs }

// SupportedLangs, desteklenen dil kodlarını döner.
func SupportedLangs() []string { return append([]string(nil), supportedLangs...) }

// isSupportedLang, verilen kodun kataloğu var mı.
func isSupportedLang(lang string) bool {
	_, ok := catalogs[lang]
	return ok
}

// translate, kataloğdan metni alır; anahtar yoksa anahtarın kendisini döner
// (sayfa boş metinle bozulmaz, eksik anahtar gözle görülür). args verilirse
// metin fmt.Sprintf ile biçimlenir.
func translate(lang, key string, args ...any) string {
	c, ok := catalogs[lang]
	if !ok {
		c = catalogs[DefaultLang]
	}
	s, ok := c[key]
	if !ok {
		if fb, ok2 := catalogs[DefaultLang][key]; ok2 {
			s = fb
		} else {
			return key
		}
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// resolveLang, dil tercihini çözer: ?lang= > cookie > Accept-Language >
// DefaultLang. İkinci dönüş değeri, tercihin cookie'ye yazılması gerekip
// gerekmediğini söyler (yalnız ?lang= ile geldiğinde true).
func resolveLang(r *http.Request) (lang string, persist bool) {
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lang"))); q != "" {
		if isSupportedLang(q) {
			return q, true
		}
	}
	if c, err := r.Cookie(cookieLang); err == nil && isSupportedLang(c.Value) {
		return c.Value, false
	}
	if l := negotiateLang(r.Header.Get("Accept-Language")); l != "" {
		return l, false
	}
	return DefaultLang, false
}

// negotiateLang, Accept-Language başlığından desteklenen en yüksek q
// değerli dili seçer; eşleşme yoksa boş döner.
func negotiateLang(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cands []cand
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if idx := strings.Index(part, ";"); idx >= 0 {
			tag = strings.TrimSpace(part[:idx])
			for _, p := range strings.Split(part[idx+1:], ";") {
				p = strings.TrimSpace(p)
				if v, ok := strings.CutPrefix(p, "q="); ok {
					if f, err := parseFloat(v); err == nil {
						q = f
					}
				}
			}
		}
		// "tr-TR" -> "tr"; "*" atlanır.
		base := strings.ToLower(tag)
		if idx := strings.Index(base, "-"); idx > 0 {
			base = base[:idx]
		}
		if !isSupportedLang(base) || q <= 0 {
			continue
		}
		cands = append(cands, cand{base, q, i})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].q != cands[j].q {
			return cands[i].q > cands[j].q
		}
		return cands[i].pos < cands[j].pos
	})
	return cands[0].lang
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f, err
}
