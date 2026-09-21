package profanity

import "testing"

// TestContains, sözlük tablo testi — en az 8 küfürlü TR/EN örnek (sansürlü
// varyantlar dahil) yakalanmalı, en az 6 masum örnek YAKALANMAMALI (#100
// spec). Girdi olarak metinde geçen küfür kelimesi bilerek loga/commit'e
// yazılmıyor — yalnız test dosyasında, kod içinde kalıyor.
func TestContains(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// --- küfürlü / ağır hakaret (want=true), sansürlü varyantlar dahil ---
		{"en düz küfür", "this app is fucking broken", true},
		{"en sansürlü yıldız (u->*)", "this app is f*cking broken", true},
		{"en sansürlü çift yıldız", "what an assh*le move", true},
		{"en dolar ikamesi", "such a$$hole behavior", true},
		{"en nokta ayraçlı", "get this s.h.i.t fixed", true},
		{"en ağır hakaret", "the support team are total idiots", true},
		{"tr düz küfür", "siktir git bu uygulamadan", true},
		{"tr nokta ayraçlı", "s.i.k.t.i.r artık bu hatadan", true},
		{"tr büyük harf", "SALAK bir tasarım yapmışlar", true},
		{"tr ağır hakaret", "ne gerizekalı bir karar", true},

		{"en buyuk harf shit", "THIS APP IS SHIT", true},
		{"en buyuk harf idiot", "WHAT AN IDIOT MOVE", true},
		{"en buyuk harf fucking ek", "THIS IS FUCKING BROKEN", true},
		{"en karisik harf idiot", "Idiot move.", true},
		{"en buyuk harf bitch", "BITCH please", true},
		{"en buyuk harf idiot 2", "such an IDIOT decision", true},

		// --- masum (want=false) — kelime sınırı testi ---
		{"tr klasik", "bu klasik bir tasarım hatası", false},
		{"tr şikayet", "kullanıcı şikayet formu dolduramıyor", false},
		{"tr sıkıntı", "ödeme akışında bir sıkıntı var", false},
		{"tr sikke", "ödeme dijital sikke ile yapılıyor", false},
		{"en essex", "the team is based in Essex", false},
		{"en assess", "let's assess the technical risk", false},
		{"en scunthorpe", "the conference was held in Scunthorpe", false},

		{"tr sik sik kucuk harf", "Uygulama sık sık çöküyor", false},
		{"tr cok sik kullanilan", "Bu çok sık kullanılan bir özellik", false},
		{"tr sik sik hata", "Sık sık hata alıyorum", false},
		{"tr SIK SIK buyuk harf belirsiz", "SIK SIK donuyor", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Contains(c.in)
			if got != c.want {
				t.Errorf("Contains(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestFilterDropsMatchingLines, Filter'ın eşleşen satırı MASKELEMEDEN
// tamamen attığını, kalanları değiştirmeden koruduğunu doğrular.
func TestFilterDropsMatchingLines(t *testing.T) {
	in := []string{
		"this is a clean quote",
		"this app is fucking broken",
		"bu klasik bir tasarım hatası",
		"siktir git bu uygulamadan",
	}
	got := Filter(in)
	want := []string{
		"this is a clean quote",
		"bu klasik bir tasarım hatası",
	}
	if len(got) != len(want) {
		t.Fatalf("Filter() = %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Filter()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFilterNeverReturnsNil, nil-slice tuzağı guard'ı: girdi nil olsa da,
// tüm satırlar elense de dönüş değeri boş dilimdir (NULL değil).
func TestFilterNeverReturnsNil(t *testing.T) {
	if got := Filter(nil); got == nil {
		t.Fatal("Filter(nil) = nil, want boş (non-nil) dilim")
	}
	all := []string{"this is fucking broken", "siktir git"}
	got := Filter(all)
	if got == nil {
		t.Fatal("Filter(tümü elenen) = nil, want boş (non-nil) dilim")
	}
	if len(got) != 0 {
		t.Fatalf("Filter(tümü elenen) len = %d, want 0", len(got))
	}
}
