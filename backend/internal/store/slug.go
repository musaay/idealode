package store

import (
	"crypto/rand"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// turkishTranslit, Türkçe harflerin ASCII karşılıklarını eşler (küçük harf
// hedefine). lower() ÖNCESİ uygulanır ki "İ" (noktalı büyük I) de doğru
// dönüşsün — Go'nun strings.ToLower'ı zaten Unicode-doğru olsa da eşleme
// burada açık tutulur (SQL backfill'deki translate() tablosuyla aynı ilke).
var turkishTranslit = strings.NewReplacer(
	"ç", "c", "Ç", "c",
	"ş", "s", "Ş", "s",
	"ğ", "g", "Ğ", "g",
	"ü", "u", "Ü", "u",
	"ö", "o", "Ö", "o",
	"ı", "i", "İ", "i",
)

// slugNonAlnum, [a-z0-9] dışındaki her karakter dizisini tek bir '-' ile
// değiştirir.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugTitleMaxRunes, slug tabanına giren başlık kısmının üst sınırı (#110).
const slugTitleMaxRunes = 60

// slugify, kart başlığından URL-güvenli bir taban üretir: ilk 60 karakter,
// Türkçe harf transliterasyonu (ç/ş/ğ/ü/ö/ı), küçük harf, yalnız
// [a-z0-9-]. Boşa düşen başlık (yalnız noktalama/emoji vb., ya da boş
// başlık) "kart" döner — edge case, spec.
func slugify(title string) string {
	t := []rune(strings.TrimSpace(title))
	if len(t) > slugTitleMaxRunes {
		t = t[:slugTitleMaxRunes]
	}
	s := strings.ToLower(turkishTranslit.Replace(string(t)))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "kart"
	}
	return s
}

// slugSuffixAlphabet, rastgele ekin karakter kümesi: base32-ish (32 karakter,
// 5 bit/karakter), karışan 0/O/1/l/I yok.
const slugSuffixAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// slugSuffixLen, rastgele ekin uzunluğu.
const slugSuffixLen = 4

// randomSlugSuffix, kriptografik rastgele 4 karakterlik ek üretir.
// crypto/rand.Read Go 1.24'ten beri hata dönmez (bkz. web/session.go'daki
// aynı gerekçe); yine de dönüş değeri savunmacı biçimde yok sayılmaz —
// rastgelelik kaynağı yoksa sabit (ama en azından deterministik olmayan
// başlangıç durumuna göre değişen) bir dizilime düşülür, insert katmanı
// zaten UNIQUE çakışmasında yeniden dener.
func randomSlugSuffix() string {
	b := make([]byte, slugSuffixLen)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i)
		}
	}
	out := make([]byte, slugSuffixLen)
	for i, v := range b {
		out[i] = slugSuffixAlphabet[int(v)%len(slugSuffixAlphabet)]
	}
	return string(out)
}

// slugSuffixFn, ekin üretimini dolaylılaştırır — yalnız testler çakışma/
// yeniden deneme senaryosunu deterministik kurmak için geçici olarak
// değiştirir (bkz. store paketindeki slug_test.go); üretimde her zaman
// randomSlugSuffix'tir.
var slugSuffixFn = randomSlugSuffix

// newSlug, taban + rastgele ek birleşimini döner. Çakışmada (UNIQUE ihlali)
// çağıran yeni bir ek için tekrar çağırır — bkz. InsertIdea/InsertBlendedIdea
// ve isSlugConflict.
func newSlug(title string) string {
	return slugify(title) + "-" + slugSuffixFn()
}

// isSlugConflict, insert hatasının ideas.slug UNIQUE kısıtına (bkz. migration
// 016) çarptığını söyler — yalnız bu durumda çağıran yeni bir rastgele ekle
// yeniden dener. Başka bir hata (bağlantı, CHECK ihlali vb.) olduğu gibi
// yukarı sarılır.
func isSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "ideas_slug_unique"
}
