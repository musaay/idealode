package store

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestSlugify, Türkçe transliterasyon + küçük harf + [a-z0-9-] kuralını ve
// edge case'leri (#110) tablo halinde doğrular. DB gerektirmez.
func TestSlugify(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"düz türkçe", "Küçük İşletmeler İçin Çağrı Yönlendirme", "kucuk-isletmeler-icin-cagri-yonlendirme"},
		{"noktalama ve boşluk", "  Merhaba, Dünya!  ", "merhaba-dunya"},
		{"tamamen noktalama -> kart", "!!! ???", "kart"},
		{"boş başlık -> kart", "", "kart"},
		{"yalnız boşluk -> kart", "   ", "kart"},
		{"büyük İ ve ı", "İstanbul Iıİi", "istanbul-iiii"},
		{"ardışık noktalama tek tireye iner", "a---b   c", "a-b-c"},
		{"baştaki/sondaki noktalama kırpılır", "-başlık-", "baslik"},
		{"60 karakter sınırı", strings.Repeat("a", 80), strings.Repeat("a", 60)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := slugify(c.title); got != c.want {
				t.Errorf("slugify(%q) = %q, beklenen %q", c.title, got, c.want)
			}
		})
	}
}

// TestSlugifyOnlyAllowedChars, üretilen tabanın [a-z0-9-] dışında karakter
// taşımadığını çeşitli girdilerde (emoji, Kiril, karışık) doğrular.
func TestSlugifyOnlyAllowedChars(t *testing.T) {
	inputs := []string{
		"emoji 🚀 başlık",
		"Кириллица тест",
		"Mixed ÇŞĞÜÖI Кириллица 123",
		strings.Repeat("ğ", 100),
	}
	allowed := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
	}
	for _, in := range inputs {
		got := slugify(in)
		if got == "" {
			t.Errorf("slugify(%q) boş döndü", in)
			continue
		}
		for _, r := range got {
			if !allowed(r) {
				t.Errorf("slugify(%q) = %q, izin verilmeyen karakter: %q", in, got, r)
				break
			}
		}
	}
}

// TestRandomSlugSuffix, ekin uzunluğunu, karakter kümesini (0/o/1/l yok) ve
// art arda çağrıların farklı değer ürettiğini doğrular.
func TestRandomSlugSuffix(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s := randomSlugSuffix()
		if len(s) != slugSuffixLen {
			t.Fatalf("uzunluk = %d, beklenen %d (%q)", len(s), slugSuffixLen, s)
		}
		for _, r := range s {
			if r == '0' || r == 'o' || r == '1' || r == 'l' {
				t.Errorf("karışan karakter üretildi: %q içinde %q", s, r)
			}
			if !strings.ContainsRune(slugSuffixAlphabet, r) {
				t.Errorf("alfabede olmayan karakter: %q içinde %q", s, r)
			}
		}
		seen[s] = true
	}
	if len(seen) < 45 {
		t.Errorf("50 çağrıda yalnız %d benzersiz değer üretildi — rastgelelik şüpheli", len(seen))
	}
}

// TestIsSlugConflict, yalnız ideas_slug_unique kısıtına çarpan 23505
// hatasının "çakışma" sayıldığını; başka kod/kısıt veya sarılmamış hataların
// sayılmadığını doğrular. DB gerektirmez (sentetik pgconn.PgError).
func TestIsSlugConflict(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"slug unique ihlali", &pgconn.PgError{Code: "23505", ConstraintName: "ideas_slug_unique"}, true},
		{"başka unique kısıtı", &pgconn.PgError{Code: "23505", ConstraintName: "ideas_pkey"}, false},
		{"unique dışı kod", &pgconn.PgError{Code: "23503", ConstraintName: "ideas_slug_unique"}, false},
		{"pg hatası değil", errFake("bağlantı koptu"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSlugConflict(c.err); got != c.want {
				t.Errorf("isSlugConflict(%v) = %v, beklenen %v", c.err, got, c.want)
			}
		})
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
