// Package prefilter, LLM çağrısı öncesi maliyet azaltma katmanı: bariz
// noise post'ları basit keyword heuristic ile eler (plan madde 4).
//
// Kalıplar iki dillidir: global kaynaklar (HN, GitHub) İngilizce, TR
// kaynakları (Technopat) Türkçe üretir — Faz 1 rescope, TR pazarı odağı.
package prefilter

import "strings"

// signalPatterns: talep/acı sinyali taşıyan kalıplar. Tek yerde durur,
// eşik iterasyonu için kolay genişletilebilir.
var signalPatterns = []string{
	"i wish",
	"is there a tool",
	"is there an app",
	"is there a way",
	"is there any",
	"why doesn't",
	"why isn't there",
	"why is there no",
	"frustrated with",
	"frustrating",
	"looking for a tool",
	"looking for an app",
	"looking for software",
	"any alternative to",
	"alternative to",
	"recommend a tool",
	"recommendation for",
	"does anyone know a tool",
	"somebody should build",
	"someone should build",
	"someone should make",
	"tired of",
	"pain point",
	"there's no good way",
	"no good tool",
	"wish there was",
	"can't find a tool",
	"cannot find a tool",
	"struggling to find",

	// Türkçe talep/acı kalıpları (TR kaynakları için)
	"var mı",
	"neden yok",
	"keşke",
	"arıyorum",
	"tavsiye",
	"öneri",
	"önerir misiniz",
	"bıktım",
	"sorun yaşıyorum",
	"çözemedim",
	"çözüm bulamadım",
	"alternatif",
	"nasıl yapabilirim",
	"bir uygulama",
	"bir program",
}

// bypassPlatforms: içeriği doğal filtrelenmiş kaynaklar — softwarerecs'in
// tamamı "şu işi yapan araç var mı?" soruları olduğu için SE atlanmaz;
// googleplay connector'ı yalnızca <=3 yıldızlı yorumları çektiği için
// içerik zaten şikayet ağırlıklıdır, doğrudan analize gider.
var bypassPlatforms = map[string]bool{
	"stackexchange": true,
	"googleplay":    true,
}

// ShouldAnalyze, post'un LLM classification'a gitmeye değer olup olmadığını
// söyler.
func ShouldAnalyze(platform, title, body string) bool {
	if bypassPlatforms[platform] {
		return true
	}
	text := strings.ToLower(title + " " + body)
	for _, p := range signalPatterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}
