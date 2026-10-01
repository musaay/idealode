package pipeline

import (
	"reflect"
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/config"
)

// lensset_test.go (#197): newLensSet/lensSetFor/skippedThirdPartyVerdict'in
// DB'siz birim testleri — üçüncü-taraf prompt sürümü seçimi, organik ve tohum
// yolunun AYNI tanımı kullanması (#123), revenue ayıklamasının kimlikle
// yapılması (v3'te de üçüncü-taraf atlanır) ve env boşken bugünkü tanımın
// BİREBİR korunması.

// TestNewLensSetEmptyConfigIsToday: env boşken (THIRD_PARTY_PROMPT=v1,
// THIRD_PARTY_VOTES=1 — test config'inde ""/0) mercek tanımı bugünküyle
// BİREBİR aynıdır: üçüncü-taraf v1 sistem+"v1", oy sayısı tek çağrı; paket
// düzeyi seedLenses/trendingLenses/revenueLenses bu tanımın görünümüdür.
func TestNewLensSetEmptyConfigIsToday(t *testing.T) {
	for _, set := range []lensSet{newLensSet("", 0), newLensSet("v1", 1), lensSetFor(&config.Config{}), defaultLensSet} {
		if !reflect.DeepEqual(set.organic, seedLenses) {
			t.Errorf("organic seedLenses ile aynı olmalı: %+v", set.organic)
		}
		if !reflect.DeepEqual(set.trending, trendingLenses) {
			t.Errorf("trending trendingLenses ile aynı olmalı: %+v", set.trending)
		}
		if !reflect.DeepEqual(set.revenue, revenueLenses) {
			t.Errorf("revenue revenueLenses ile aynı olmalı: %+v", set.revenue)
		}
	}

	set := newLensSet("", 0)
	wantThird := seedLens{id: lensIDThirdParty, name: "üçüncü-taraf inşa edilebilirlik", system: lensThirdPartySystem, version: "v1", votes: 1}
	if set.organic[0] != wantThird || set.thirdParty != wantThird {
		t.Errorf("üçüncü-taraf mercek v1/tek-oy olmalı: %+v", set.organic[0])
	}
	wantData := seedLens{id: lensIDDataAccess, name: "veri-erişimi", system: lensDataAccessSystem, version: "v1"}
	if set.organic[1] != wantData {
		t.Errorf("veri-erişimi mercek DEĞİŞMEMELİ (oy yok): %+v", set.organic[1])
	}
	if len(set.organic) != 2 || len(set.trending) != 3 || len(set.revenue) != 1 {
		t.Errorf("mercek sayıları 2/3/1 olmalı: %d/%d/%d", len(set.organic), len(set.trending), len(set.revenue))
	}
	if set.trending[0].id != lensIDProductizable || set.trending[0].votes > 1 {
		t.Errorf("trending[0] ürünleştirilebilirlik (tek çağrı) olmalı: %+v", set.trending[0])
	}
}

// TestNewLensSetV3: THIRD_PARTY_PROMPT=v3 -> üçüncü-taraf v3 sistem prompt'u
// ve "v3" etiketi (organik + trending); veri-erişimi/ürünleştirilebilirlik
// DEĞİŞMEZ; gelir yolu v3'te DE üçüncü-tarafı atlar (kimlikle ayıklanır,
// system metniyle değil — v3 metni lensThirdPartySystem'e eşit olmadığı için
// metinle ayıklama burada kırılırdı).
func TestNewLensSetV3(t *testing.T) {
	set := newLensSet("v3", 3)

	if set.organic[0].system != lensThirdPartySystemV3 || set.organic[0].version != "v3" || set.organic[0].votes != 3 {
		t.Errorf("organic[0] v3 sistem + \"v3\" + 3 oy olmalı: %+v", set.organic[0])
	}
	if set.trending[1] != set.organic[0] {
		t.Errorf("trending organic'in AYNI üçüncü-taraf tanımını taşımalı (#123): %+v vs %+v", set.trending[1], set.organic[0])
	}
	if set.thirdParty != set.organic[0] {
		t.Errorf("thirdParty seçili mercek olmalı: %+v", set.thirdParty)
	}
	if set.organic[1] != seedLenses[1] || set.trending[0] != trendingLenses[0] || set.trending[2] != seedLenses[1] {
		t.Errorf("veri-erişimi/ürünleştirilebilirlik v3'ten ETKİLENMEMELİ")
	}

	if len(set.revenue) != 1 || set.revenue[0].id != lensIDDataAccess {
		t.Fatalf("revenue yalnız veri-erişimi olmalı (v3'te de üçüncü-taraf atlanır): %+v", set.revenue)
	}
	for _, l := range set.revenue {
		if l.id == lensIDThirdParty || l.system == lensThirdPartySystemV3 || l.system == lensThirdPartySystem {
			t.Errorf("revenue üçüncü-taraf merceği İÇERMEMELİ: %+v", l)
		}
	}
	// v3 sistem metni v1'den farklı — kimlik tabanlı ayıklamanın gerekliliği.
	if lensThirdPartySystemV3 == lensThirdPartySystem {
		t.Fatal("v1 ve v3 metni aynı olmamalı")
	}
}

// TestNewLensSetUnknownPromptFallsBackToV1: v3 dışındaki her değer (boş,
// "v2", büyük harf) v1 — config.Load zaten yalnız "v1"/"v3" döner, burası
// savunmacı.
func TestNewLensSetUnknownPromptFallsBackToV1(t *testing.T) {
	for _, pv := range []string{"", "v1", "v2", "V3", "x"} {
		set := newLensSet(pv, 1)
		if set.organic[0].system != lensThirdPartySystem || set.organic[0].version != "v1" {
			t.Errorf("prompt=%q: v1 beklenirdi: %+v", pv, set.organic[0])
		}
	}
}

// TestNewLensSetVotesOnlyOnThirdParty: oy sayısı YALNIZ üçüncü-taraf
// merceğine uygulanır (veri-erişimi/ürünleştirilebilirlik tek çağrı kalır),
// <1 değer 1'e iner.
func TestNewLensSetVotesOnlyOnThirdParty(t *testing.T) {
	set := newLensSet("v1", 5)
	if set.organic[0].votes != 5 {
		t.Errorf("üçüncü-taraf 5 oy olmalı: %+v", set.organic[0])
	}
	for _, l := range append(append([]seedLens{}, set.trending...), set.revenue...) {
		if l.id != lensIDThirdParty && l.votes > 1 {
			t.Errorf("%s oylanmamalı: %+v", l.id, l)
		}
	}
	if got := newLensSet("v1", -2).organic[0].votes; got != 1 {
		t.Errorf("negatif oy 1'e inmeli, geldi %d", got)
	}
}

// TestLensSetForUsesConfig: lensSetFor config'in iki alanını okur — organik ve
// tohum yolları AYNI fonksiyonu çağırdığından (SynthesizeIdeas/ProcessSeeds)
// ikisi de aynı tanımı görür.
func TestLensSetForUsesConfig(t *testing.T) {
	set := lensSetFor(&config.Config{ThirdPartyPrompt: "v3", ThirdPartyVotes: 3})
	if set.thirdParty.system != lensThirdPartySystemV3 || set.thirdParty.version != "v3" || set.thirdParty.votes != 3 {
		t.Errorf("config'ten v3/3 oy beklenirdi: %+v", set.thirdParty)
	}
	if !reflect.DeepEqual(set, newLensSet("v3", 3)) {
		t.Error("lensSetFor(cfg) == newLensSet(prompt, votes) olmalı")
	}
}

// TestSkippedThirdPartyVerdictCarriesSelectedVersion (#167, #197): gelir
// tohumunda atlanan üçüncü-taraf merceğinin "skipped" kaydı SEÇİLİ sürümü
// taşır (v3 seçiliyse "v3"), blok/LLM izi yok (Model boş), subject=seed.
func TestSkippedThirdPartyVerdictCarriesSelectedVersion(t *testing.T) {
	for _, tc := range []struct{ prompt, wantVersion string }{{"", "v1"}, {"v1", "v1"}, {"v3", "v3"}} {
		set := newLensSet(tc.prompt, 3)
		v := skippedThirdPartyVerdict(set.thirdParty)
		if v.Verdict != "skipped" || v.Lens != "üçüncü-taraf inşa edilebilirlik" || v.Subject != "seed" {
			t.Errorf("prompt=%q: skipped/üçüncü-taraf/seed beklenirdi: %+v", tc.prompt, v)
		}
		if v.PromptVersion != tc.wantVersion {
			t.Errorf("prompt=%q: PromptVersion %q beklenirdi, geldi %q", tc.prompt, tc.wantVersion, v.PromptVersion)
		}
		if v.Model != "" || !strings.Contains(v.Reason, "#167") {
			t.Errorf("prompt=%q: Model boş + Reason #167 referanslı olmalı: %+v", tc.prompt, v)
		}
		if v.At.IsZero() {
			t.Errorf("prompt=%q: At dolu olmalı", tc.prompt)
		}
	}
}
