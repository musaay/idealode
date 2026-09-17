// Package profanity, kart alıntılarında (example_quotes, local_evidence)
// küfür/ağır hakaret içeren satırları tespit eder (#100). Saf pakettir —
// DB/HTTP bilmez; tek garanti noktası olarak store yazım sınırında
// (InsertIdea, InsertBlendedIdea, SetIdeaLocalEvidence,
// AppendIdeaLocalEvidence) çağrılır.
//
// Eşleşen satır ATILIR, maskelenmez — alıntılar birebir tutulur ilkesi
// (kısmi/maskelenmiş alıntı yanlış izlenim verir).
package profanity

import (
	"regexp"
	"strings"
	"unicode"
)

// foldRune, karşılaştırma öncesi TEK bir rune'u normalize eder.
//
// 'ı' (noktasız i, U+0131) KASITLI OLARAK 'i'ye KATLANMAZ (reviewer bulgusu,
// #100): bu katlama "sık" (frequent) gibi çok yaygın masum kelimeleri
// "sik" köküyle birebir eşleştiriyordu ("Uygulama sık sık çöküyor" gibi
// masum alıntılar düşüyordu). ı/i ayrımı METİN tarafında KORUNUR; sözlük
// tarafında 'ı' içeren kelimeler wordPattern'de [ıi] sınıfıyla yazılır (hem
// doğru hem ASCII-yazım varyantını yakalasın diye) — ama sözlükteki düz 'i'
// yalnız metindeki 'i' ile eşleşir, 'ı' ile eşleşmez.
//
// 'I' (ASCII büyük I, U+0049) Türkçe klavye kuralına göre 'ı'nın büyük
// harfidir (Türkçe'de 'i'nin büyüğü noktalı 'İ'dir) — bu yüzden 'ı'ya
// katlanır. Bunun sonucu: "SIK SIK donuyor" gibi tümü büyük harfle yazılmış
// metinler "sık" olarak okunur, küfür kökü "sik" ile EŞLEŞMEZ (yakalanmama
// yönünde hata payı tercih edilir — yanlış pozitif, yakalanmayan bir
// küfürden daha kötü sonuç doğurur). Gerçek küfür "sik" büyük harfle
// yazılacaksa Türkçe kuralına uygun noktalı 'İ' ile ("SİK") yazılmalıdır;
// bu durumda foldRune 'İ'yi 'i'ye çevirir ve normal şekilde yakalanır.
func foldRune(r rune) rune {
	switch r {
	case 'I':
		return 'ı'
	case 'İ':
		return 'i'
	case 'Ş', 'ş':
		return 's'
	case 'Ç', 'ç':
		return 'c'
	case 'Ğ', 'ğ':
		return 'g'
	case 'Ö', 'ö':
		return 'o'
	case 'Ü', 'ü':
		return 'u'
	case 'ı':
		return 'ı' // korunur — bkz. yukarıdaki fonksiyon yorumu
	default:
		return unicode.ToLower(r)
	}
}

// leetFold, yaygın sansür ikameleri: rakam-harf ve sembol-harf değişimleri
// ("rakam-harf ikameleri gibi yaygın olanlar", #100 spec). '1'->'i' kasıtlı
// yazımları ("s1k") yakalar — kabul edilen davranış.
var leetFold = map[rune]rune{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't',
	'@': 'a', '$': 's',
}

// isSeparator, saf ayraç sayılan ve normalize()'da tamamen atılan sansür
// karakterleri — harfler arasına serpiştirilen nokta/alt çizgi/tire
// ("s.i.k.t.i.r" gibi). '*' burada YOK: o normalize()'da korunur, tek harf
// yerine geçen joker olarak wordPattern'de ayrıca ele alınır.
func isSeparator(r rune) bool {
	return r == '.' || r == '_' || r == '-'
}

// normalize, karşılaştırma için metni tek biçime indirger: küçük harf +
// Türkçe karakter katlama (ı/i AYRIMI KORUNARAK, bkz. foldRune) + yaygın
// sansür ikameleri + ayraç temizliği.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		r = foldRune(r)
		if f, ok := leetFold[r]; ok {
			r = f
		}
		if isSeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// suffixPattern, kök kelimeden sonra izin verilen tek TÜRETİM ekini üretir
// ("fuck" -> "fucking", "idiot" -> "idiots") — rastgele devam eden harflere
// İZİN VERİLMEZ, yoksa kelime sınırının önlediği Scunthorpe problemi
// (assess, Essex, klasik, şikayet, sıkıntı) geri döner. Liste kasıtlı kısa
// tutulur. isEN=true iken "ing" ekindeki 'i' de [iı] sınıfıyla yazılır —
// aksi halde büyük harfli İngilizce küfürler ("FUCKING") kaçırılıyordu
// (reviewer 2. tur bulgusu, #100): 'I' foldRune'da 'ı'ya katlandığından
// "FUCKING" -> "fuckıng" oluyor, düz "ing" bunu yakalayamıyordu.
func suffixPattern(isEN bool) string {
	ing := "ing"
	if isEN {
		ing = `[iı]ng`
	}
	return ing + `|ed|er|s|y`
}

// wordBoundaryPre/wordBoundaryPost, Go'nun regexp paketindeki `\b`in YERİNE
// kullanılır: RE2'nin `\b`i yalnız ASCII [0-9A-Za-z_] karakterlerini "harf"
// sayar, Türkçe harfler (ı, ş, ğ, ç, ö, ü) \W (harf-dışı) kabul edilir — bu
// yüzden "gerizekalı" gibi Türkçe harfle BİTEN bir sözlük kelimesinde
// `\b` sondan hiç eşleşmez (reviewer testiyle doğrulandı). Bunun yerine
// Unicode harf/rakam sınıfını (\p{L}, \p{N}) temel alan elle sınır
// kullanılır — dize başı/sonu ya da harf-olmayan bir karakter.
const wordBoundaryPre = `(?:^|[^\p{L}\p{N}_])`
const wordBoundaryPost = `(?:[^\p{L}\p{N}_]|$)`

// wordPattern, sözlükteki tek bir kelime için kelime-sınırlı desen üretir:
// her harf ya birebir ya da tek bir '*' joker ile eşleşebilir — "fuck" hem
// "fuck" hem "f*ck" hem "fu*k" gibi sansürlü varyantları yakalar; ardından
// en fazla bir yaygın türetim eki (suffixPattern) izin verilir. Ayraçlar
// (., _, -) normalize() aşamasında zaten atıldığından burada ele alınmaz.
//
// Sözlükteki 'ı' harfi HER ZAMAN metinde hem 'ı' hem 'i' ile eşleşir (doğru
// Türkçe yazım + yaygın ASCII-yazım varyantı, örn. "gerizekalı"/
// "gerizekali").
//
// Sözlükteki düz 'i' harfinin davranışı isEN'e göre DEĞİŞİR:
//   - isEN=false (TR sözlük): YALNIZ metindeki 'i' ile eşleşir, 'ı' ile
//     eşleşmez — "sık" (frequent) gibi masum kelimelerin "sik" köküyle
//     yanlış eşleşmesini önler (reviewer 1. tur bulgusu).
//   - isEN=true (EN sözlük): metinde hem 'i' HEM 'ı' ile eşleşir — çünkü
//     foldRune, ASCII 'I' harfini Türkçe klavye kuralına göre 'ı'ya katlar;
//     bu yüzden BÜYÜK HARFLE yazılmış İngilizce küfürler ("SHIT", "IDIOT",
//     "FUCKING", "BITCH") normalize sonrası 'ı' içerir ve düz 'i' ile
//     eşleşmezdi (reviewer 2. tur bulgusu — fail-open regresyon).
//
// Kelime sınırı klasik "Scunthorpe problemi"ni önler: "klasik"/"şikayet"/
// "sıkıntı"/"assess"/"Scunthorpe" gibi kelimelerin İÇİNDE geçen bir sözlük
// kelimesi, o kelime tüm token'ı (+ izinli ek) KAPLAMADIKÇA eşleşmez.
func wordPattern(w string, isEN bool) string {
	var b strings.Builder
	b.WriteString(wordBoundaryPre)
	for _, r := range w {
		b.WriteString(`(?:`)
		switch {
		case r == 'ı':
			b.WriteString(`[ıi]`)
		case r == 'i' && isEN:
			b.WriteString(`[iı]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
		b.WriteString(`|\*)`)
	}
	b.WriteString(`(?:`)
	b.WriteString(suffixPattern(isEN))
	b.WriteString(`)?`)
	b.WriteString(wordBoundaryPost)
	return b.String()
}

// compiled, sözlükteki tüm kelimelerin derlenmiş desenleri — paket
// yüklenirken bir kez hazırlanır (Contains/Filter sık çağrılır). TR ve EN
// listeleri AYRI derlenir: 'i' harfinin davranışı dile göre değişiyor (bkz.
// wordPattern), bu yüzden hangi kelimenin hangi listeden geldiği önemli.
var compiled = append(compileWords(profaneWordsTR, false), compileWords(profaneWordsEN, true)...)

func compileWords(words []string, isEN bool) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		out = append(out, regexp.MustCompile(wordPattern(w, isEN)))
	}
	return out
}

// Contains, s içinde sözlükteki herhangi bir kelimenin (sansürlü varyantlar
// dahil) kelime sınırına oturarak geçip geçmediğini döner.
func Contains(s string) bool {
	n := normalize(s)
	for _, re := range compiled {
		if re.MatchString(n) {
			return true
		}
	}
	return false
}

// Filter, lines içindeki küfür/ağır hakaret içeren satırları ATAR
// (maskelemez) — kalan satırlar orijinal sırayla, değiştirilmeden döner.
// Dönüş değeri ASLA nil değildir: NOT NULL DEFAULT '{}' kolonlarına
// (example_quotes, local_evidence) doğrudan yazılabilir (CLAUDE.md
// nil-slice tuzağı) — girdi nil ya da tümü elense bile boş dilim döner.
func Filter(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if Contains(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}
