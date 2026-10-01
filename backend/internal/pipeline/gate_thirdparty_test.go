package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/store"
)

// ---------------------------------------------------------------------
// voteLens (#197) — genel N-oy çekirdeğinin üçüncü-taraf yüklemiyle
// (verdict=="fail", criterion yok) tablo testleri. seqVoteCall gate_test.go'da.

// TestVoteLensThirdPartyPredicateTable: üçüncü-taraf yüklemiyle (isThirdPartyBlock)
// N=3 oy dizileri — oybirliği blok, ayrışma tartışmalı, erken çıkış, hata
// vakaları. Tartışmalı gerekçe biçimi özgünlükle AYNI: "tartışmalı: <blok>/<k>
// oy blok — <ilk blok oyun gerekçesi>".
func TestVoteLensThirdPartyPredicateTable(t *testing.T) {
	fail := func(r string) lensVerdict { return lensVerdict{Verdict: "fail", Criterion: "none", Reason: r} }
	pass := func(r string) lensVerdict { return lensVerdict{Verdict: "pass", Criterion: "none", Reason: r} }
	unsure := func(r string) lensVerdict { return lensVerdict{Verdict: "unsure", Criterion: "none", Reason: r} }

	cases := []struct {
		name         string
		n            int
		votes        []lensVerdict
		errAt        int
		wantCalls    int
		wantBlocked  bool
		wantDisputed bool
		wantErr      bool
		wantVerdict  string
		wantReason   string
	}{
		{"n=1 fail blok", 1, []lensVerdict{fail("a")}, -1, 1, true, false, false, "fail", "a"},
		{"n=1 pass", 1, []lensVerdict{pass("a")}, -1, 1, false, false, false, "pass", "a"},
		{"n=3 fail,fail,fail oybirliği blok (gerekçe İLK oyunki)", 3, []lensVerdict{fail("a"), fail("b"), fail("c")}, -1, 3, true, false, false, "fail", "a"},
		{"n=3 ilk oy pass -> 1 çağrı, blok yok, tartışma yok", 3, []lensVerdict{pass("a")}, -1, 1, false, false, false, "pass", "a"},
		{"n=3 ilk oy unsure -> 1 çağrı, tartışma DEĞİL", 3, []lensVerdict{unsure("a")}, -1, 1, false, false, false, "unsure", "a"},
		{"n=3 fail,pass -> tartışmalı, 2 çağrı", 3, []lensVerdict{fail("a"), pass("b")}, -1, 2, false, true, false, "unsure", "tartışmalı: 1/2 oy blok — a"},
		{"n=3 fail,fail,unsure -> tartışmalı 2/3", 3, []lensVerdict{fail("a"), fail("b"), unsure("c")}, -1, 3, false, true, false, "unsure", "tartışmalı: 2/3 oy blok — a"},
		{"n=3 fail,hata -> tartışmalı (Err YOK)", 3, []lensVerdict{fail("a")}, 1, 2, false, true, false, "unsure", "tartışmalı: 1/2 oy blok — a"},
		{"n=3 ilk oy hata -> Err, 1 çağrı", 3, nil, 0, 1, false, false, true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, calls := seqVoteCall(tc.votes, tc.errAt)
			decision, records := voteLens(context.Background(), tc.n, call, isThirdPartyBlock)
			if *calls != tc.wantCalls {
				t.Errorf("çağrı sayısı %d beklenirdi, geldi %d", tc.wantCalls, *calls)
			}
			if len(records) != tc.wantCalls {
				t.Errorf("oy kaydı %d beklenirdi, geldi %d", tc.wantCalls, len(records))
			}
			if (decision.Err != nil) != tc.wantErr {
				t.Fatalf("Err=%v, wantErr=%v", decision.Err, tc.wantErr)
			}
			if decision.Blocked != tc.wantBlocked || decision.Disputed != tc.wantDisputed {
				t.Errorf("Blocked/Disputed %v/%v beklenirdi, geldi %v/%v", tc.wantBlocked, tc.wantDisputed, decision.Blocked, decision.Disputed)
			}
			if decision.Verdict != tc.wantVerdict || decision.Reason != tc.wantReason {
				t.Errorf("Verdict/Reason %q/%q beklenirdi, geldi %q/%q", tc.wantVerdict, tc.wantReason, decision.Verdict, decision.Reason)
			}
		})
	}
}

// TestVoteLensPredicateDecidesWhatBlocks: aynı oy dizisi iki yüklemle farklı
// sonuç verir — özgünlükte K3 fail blok DEĞİL (k==1'de durur, blok yok),
// üçüncü-taraf merceğinde her "fail" blok (criterion yok sayılır).
func TestVoteLensPredicateDecidesWhatBlocks(t *testing.T) {
	k3 := lensVerdict{Verdict: "fail", Criterion: "K3", Reason: "talep yok"}
	votes := []lensVerdict{k3, k3, k3}

	callD, callsD := seqVoteCall(votes, -1)
	dDistinct, _ := voteLens(context.Background(), 3, callD, isDistinctivenessBlock)
	if dDistinct.Blocked || *callsD != 1 || dDistinct.Verdict != "fail" || dDistinct.Criterion != "K3" {
		t.Errorf("özgünlük yüklemi: K3 fail blok SAYILMAZ (1 çağrı, Blocked=false, kayıt için fail/K3): %+v çağrı=%d", dDistinct, *callsD)
	}

	callT, callsT := seqVoteCall(votes, -1)
	dThird, _ := voteLens(context.Background(), 3, callT, isThirdPartyBlock)
	if !dThird.Blocked || *callsT != 3 {
		t.Errorf("üçüncü-taraf yüklemi: her fail blok (3/3 oy -> Blocked, 3 çağrı): %+v çağrı=%d", dThird, *callsT)
	}
}

// TestVoteDistinctivenessWrapperUsesDistinctivenessPredicate: sarmalayıcı
// (lens-ab/test adı) voteLens + isDistinctivenessBlock ile aynı kararı verir.
func TestVoteDistinctivenessWrapperUsesDistinctivenessPredicate(t *testing.T) {
	votes := []lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "a"},
		{Verdict: "fail", Criterion: "K2", Reason: "b"},
	}
	call1, _ := seqVoteCall(votes, -1)
	call2, _ := seqVoteCall(votes, -1)
	viaWrapper, _ := voteDistinctiveness(context.Background(), 2, call1)
	viaCore, _ := voteLens(context.Background(), 2, call2, isDistinctivenessBlock)
	if viaWrapper != viaCore {
		t.Errorf("sarmalayıcı ile çekirdek aynı kararı vermeli: %+v != %+v", viaWrapper, viaCore)
	}
	if !viaWrapper.Blocked {
		t.Errorf("K1,K2 2/2 blok olmalı: %+v", viaWrapper)
	}
}

// ---------------------------------------------------------------------
// runBlockingLenses N-oy (#197) — sahte llm.Chat ile.

// scriptedLensChat, sistem prompt'una göre SIRAYLA verdict/hata döner:
// script[system][i] = i. çağrının cevabı ("pass"|"fail"|"unsure"|"err");
// liste biterse "pass". Her cevabın gerekçesi "r<k>" (k = o sistemdeki
// çağrının 1-tabanlı sırası). calls: sistem başına çağrı sayısı; temps: her
// çağrının sıcaklığı. llm.NamedChat uygular ("sahte-model").
type scriptedLensChat struct {
	script map[string][]string
	calls  map[string]int
	temps  []float64
}

func newScriptedLensChat(script map[string][]string) *scriptedLensChat {
	return &scriptedLensChat{script: script, calls: map[string]int{}}
}

func (c *scriptedLensChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *scriptedLensChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.temps = append(c.temps, temp)
	idx := c.calls[system]
	c.calls[system]++
	v := "pass"
	if seq := c.script[system]; idx < len(seq) {
		v = seq[idx]
	}
	if v == "err" {
		return "", errors.New("simulated oy hatası")
	}
	return fmt.Sprintf(`{"verdict":%q,"reason":"r%d"}`, v, idx+1), nil
}

func (c *scriptedLensChat) ModelName() string { return "sahte-model" }

// TestRunBlockingLensesThirdPartyVotesUnanimousBlocksOrganic: organik yol
// (stopOnFirstFail=true), N=3, üçüncü-taraf 3/3 fail -> mercek "fail" (blok),
// veri-erişimi HİÇ çağrılmaz; her oy ayrı kayıt, "oy k/3: " önekli; dönen
// verdicts[0] NİHAİ karar.
func TestRunBlockingLensesThirdPartyVotesUnanimousBlocksOrganic(t *testing.T) {
	set := newLensSet("v1", 3)
	chat := newScriptedLensChat(map[string][]string{
		lensThirdPartySystem: {"fail", "fail", "fail"},
	})

	outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

	if !outcome.Blocked || outcome.Stage != "blocking_lens" || outcome.Check != lensThirdPartyName {
		t.Fatalf("3/3 fail bloklamalı (blocking_lens/üçüncü-taraf): %+v", outcome)
	}
	if outcome.Reason != "r1" {
		t.Errorf("blok gerekçesi İLK oyunki (r1) olmalı, geldi %q", outcome.Reason)
	}
	if chat.calls[lensThirdPartySystem] != 3 || chat.calls[lensDataAccessSystem] != 0 {
		t.Errorf("üçüncü-taraf 3 çağrı, veri-erişimi 0 çağrı beklenirdi: %v", chat.calls)
	}
	if len(verdicts) != 1 || verdicts[0].Verdict != "fail" {
		t.Errorf("verdicts[0] NİHAİ karar (fail) olmalı, uzunluk 1: %+v", verdicts)
	}
	if len(outcome.Verdicts) != 3 {
		t.Fatalf("3 oy = 3 kalıcı kayıt beklenirdi, geldi %d: %+v", len(outcome.Verdicts), outcome.Verdicts)
	}
	for i, v := range outcome.Verdicts {
		if want := fmt.Sprintf("oy %d/3: r%d", i+1, i+1); v.Reason != want {
			t.Errorf("kayıt %d Reason %q beklenirdi, geldi %q", i, want, v.Reason)
		}
		if v.Lens != lensThirdPartyName || v.PromptVersion != "v1" || v.Subject != "card" || v.Verdict != "fail" || v.Model != "sahte-model" {
			t.Errorf("kayıt %d alanları yanlış: %+v", i, v)
		}
	}
	for _, temp := range chat.temps {
		if temp != 0 {
			t.Errorf("her oy sıcaklık 0 ile (#106) çağrılmalı, geldi %v", temp)
		}
	}
}

// TestRunBlockingLensesThirdPartyVotesDisputedDoesNotBlock: fail,pass -> mercek
// kararı "unsure"+tartışmalı, BLOKLAMAZ, veri-erişimi çağrılmaya devam eder;
// iki oy ayrı kayıt, veri-erişimi kaydı önek-SİZ.
func TestRunBlockingLensesThirdPartyVotesDisputedDoesNotBlock(t *testing.T) {
	set := newLensSet("v1", 3)
	chat := newScriptedLensChat(map[string][]string{
		lensThirdPartySystem: {"fail", "pass", "fail"},
		lensDataAccessSystem: {"pass"},
	})

	outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

	if outcome.Blocked || outcome.Err != nil || outcome.Stage != "" {
		t.Fatalf("ayrışma bloklamamalı/hata vermemeli/eleme kaydı bırakmamalı: %+v", outcome)
	}
	if chat.calls[lensThirdPartySystem] != 2 {
		t.Errorf("ayrışmada erken çıkış: üçüncü-taraf 2 çağrı (3.'sü yok), geldi %d", chat.calls[lensThirdPartySystem])
	}
	if chat.calls[lensDataAccessSystem] != 1 {
		t.Errorf("tartışmalı mercek bloklamaz: veri-erişimi çağrılmalı, geldi %d", chat.calls[lensDataAccessSystem])
	}
	if len(verdicts) != 2 {
		t.Fatalf("2 mercek kararı beklenirdi: %+v", verdicts)
	}
	if verdicts[0].Verdict != "unsure" || verdicts[0].Reason != "tartışmalı: 1/2 oy blok — r1" {
		t.Errorf("verdicts[0] unsure/tartışmalı olmalı: %+v", verdicts[0])
	}
	if verdicts[1].Verdict != "pass" {
		t.Errorf("verdicts[1] veri-erişimi pass olmalı: %+v", verdicts[1])
	}
	if len(outcome.Verdicts) != 3 {
		t.Fatalf("2 oy + 1 veri-erişimi = 3 kayıt beklenirdi: %+v", outcome.Verdicts)
	}
	if outcome.Verdicts[0].Reason != "oy 1/3: r1" || outcome.Verdicts[0].Verdict != "fail" ||
		outcome.Verdicts[1].Reason != "oy 2/3: r2" || outcome.Verdicts[1].Verdict != "pass" {
		t.Errorf("oy kayıtları yanlış: %+v", outcome.Verdicts[:2])
	}
	if da := outcome.Verdicts[2]; da.Lens != "veri-erişimi" || da.Reason != "r1" || strings.Contains(da.Reason, "oy ") {
		t.Errorf("veri-erişimi kaydı önek-SİZ ve tek çağrı olmalı: %+v", da)
	}
}

// TestRunBlockingLensesThirdPartyVotesFirstVoteError: ilk oy hata -> bugünkü
// hata yolu AYNEN (Err dolu, Check mercek adı, kalan mercekler ÇAĞRILMAZ,
// verdicts boş), hata kaydı "oy 1/3: " önekli.
func TestRunBlockingLensesThirdPartyVotesFirstVoteError(t *testing.T) {
	for _, stopOnFirstFail := range []bool{true, false} {
		set := newLensSet("v1", 3)
		chat := newScriptedLensChat(map[string][]string{lensThirdPartySystem: {"err"}})

		outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", stopOnFirstFail, "card")

		if outcome.Err == nil || outcome.Blocked || outcome.Check != lensThirdPartyName {
			t.Errorf("stopOnFirstFail=%v: Err dolu/bloksuz/Check=üçüncü-taraf beklenirdi: %+v", stopOnFirstFail, outcome)
		}
		if chat.calls[lensThirdPartySystem] != 1 || chat.calls[lensDataAccessSystem] != 0 {
			t.Errorf("stopOnFirstFail=%v: ilk oy hatasında durulmalı: %v", stopOnFirstFail, chat.calls)
		}
		if len(verdicts) != 0 {
			t.Errorf("stopOnFirstFail=%v: verdicts boş olmalı: %+v", stopOnFirstFail, verdicts)
		}
		if len(outcome.Verdicts) != 1 || outcome.Verdicts[0].Verdict != "error" ||
			!strings.HasPrefix(outcome.Verdicts[0].Reason, "oy 1/3: ") || outcome.Verdicts[0].Model != "sahte-model" {
			t.Errorf("stopOnFirstFail=%v: hata kaydı error + 'oy 1/3: ' önekli olmalı: %+v", stopOnFirstFail, outcome.Verdicts)
		}
	}
}

// TestRunBlockingLensesThirdPartyVotesLaterVoteErrorIsDisputed: blok oydan
// SONRAKİ oy hatası tartışmalı sayılır — Err YOK, bloklama YOK, kalan
// mercek çalışır; hata oyu kayıtta "error".
func TestRunBlockingLensesThirdPartyVotesLaterVoteErrorIsDisputed(t *testing.T) {
	set := newLensSet("v1", 3)
	chat := newScriptedLensChat(map[string][]string{
		lensThirdPartySystem: {"fail", "err"},
	})

	outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

	if outcome.Err != nil || outcome.Blocked {
		t.Fatalf("sonraki oy hatası Err/blok DEĞİL tartışmalıdır: %+v", outcome)
	}
	if chat.calls[lensDataAccessSystem] != 1 {
		t.Errorf("tartışmalı mercek bloklamaz, veri-erişimi çağrılmalı: %v", chat.calls)
	}
	if len(verdicts) != 2 || verdicts[0].Verdict != "unsure" || !strings.HasPrefix(verdicts[0].Reason, "tartışmalı: 1/2 oy blok") {
		t.Errorf("verdicts[0] unsure/tartışmalı olmalı: %+v", verdicts)
	}
	if len(outcome.Verdicts) != 3 || outcome.Verdicts[1].Verdict != "error" || !strings.HasPrefix(outcome.Verdicts[1].Reason, "oy 2/3: ") {
		t.Errorf("hata oyu kaydı (oy 2/3) 'error' olmalı: %+v", outcome.Verdicts)
	}
}

// TestRunBlockingLensesThirdPartyVotesFirstVotePassIsOneCall: ilk oy pass ->
// tek çağrı (N=3 maliyeti yalnız şüpheli kartta), kayıt yine "oy 1/3: " önekli.
func TestRunBlockingLensesThirdPartyVotesFirstVotePassIsOneCall(t *testing.T) {
	set := newLensSet("v1", 3)
	chat := newScriptedLensChat(map[string][]string{})

	outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

	if outcome.Blocked || outcome.Err != nil {
		t.Fatalf("pass bloklamaz: %+v", outcome)
	}
	if chat.calls[lensThirdPartySystem] != 1 {
		t.Errorf("ilk oy pass: 1 çağrı beklenirdi, geldi %d", chat.calls[lensThirdPartySystem])
	}
	if len(verdicts) != 2 || verdicts[0].Verdict != "pass" {
		t.Errorf("verdicts[0] pass olmalı: %+v", verdicts)
	}
	if len(outcome.Verdicts) != 2 || outcome.Verdicts[0].Reason != "oy 1/3: r1" {
		t.Errorf("tek oy da 'oy 1/3: ' önekli kaydedilmeli: %+v", outcome.Verdicts)
	}
}

// TestRunBlockingLensesThirdPartyVotesSeedPath: tohum yolu (stopOnFirstFail=
// false, trending seti: ürünleştirilebilirlik + üçüncü-taraf + veri-erişimi) —
// TÜM mercekler çalışır; üçüncü-taraf 3/3 fail + veri-erişimi fail -> isimler/
// sebepler birleşik; ayrışmada ise bloklanmaz.
func TestRunBlockingLensesThirdPartyVotesSeedPath(t *testing.T) {
	set := newLensSet("v1", 3)

	t.Run("oybirliği fail + veri-erişimi fail -> birleşik blok", func(t *testing.T) {
		chat := newScriptedLensChat(map[string][]string{
			lensThirdPartySystem: {"fail", "fail", "fail"},
			lensDataAccessSystem: {"fail"},
		})
		outcome, verdicts := runBlockingLenses(context.Background(), chat, set.trending, "prompt", false, "seed")
		if !outcome.Blocked {
			t.Fatalf("bloklamalı: %+v", outcome)
		}
		if want := lensThirdPartyName + ", veri-erişimi"; outcome.Check != want {
			t.Errorf("Check %q beklenirdi, geldi %q", want, outcome.Check)
		}
		if outcome.Reason != "r1; r1" {
			t.Errorf("Reason %q beklenirdi, geldi %q", "r1; r1", outcome.Reason)
		}
		if chat.calls[lensProductizableSystem] != 1 || chat.calls[lensThirdPartySystem] != 3 || chat.calls[lensDataAccessSystem] != 1 {
			t.Errorf("tüm mercekler çalışmalı (1+3+1): %v", chat.calls)
		}
		if len(verdicts) != 3 {
			t.Errorf("3 mercek kararı beklenirdi: %+v", verdicts)
		}
		// 1 (ürünleştirilebilirlik) + 3 oy + 1 veri-erişimi = 5 kayıt, hepsi subject=seed.
		if len(outcome.Verdicts) != 5 {
			t.Fatalf("5 kayıt beklenirdi: %+v", outcome.Verdicts)
		}
		for _, v := range outcome.Verdicts {
			if v.Subject != "seed" {
				t.Errorf("Subject=seed beklenirdi: %+v", v)
			}
		}
	})

	t.Run("ayrışma + veri-erişimi pass -> bloklamaz", func(t *testing.T) {
		chat := newScriptedLensChat(map[string][]string{
			lensThirdPartySystem: {"fail", "pass"},
		})
		outcome, verdicts := runBlockingLenses(context.Background(), chat, set.trending, "prompt", false, "seed")
		if outcome.Blocked || outcome.Err != nil {
			t.Fatalf("ayrışma bloklamamalı: %+v", outcome)
		}
		if chat.calls[lensDataAccessSystem] != 1 {
			t.Errorf("tüm mercekler çalışmalı: %v", chat.calls)
		}
		if len(verdicts) != 3 || verdicts[1].Verdict != "unsure" {
			t.Errorf("üçüncü-taraf nihai kararı (verdicts[1]) unsure olmalı: %+v", verdicts)
		}
	})
}

// TestRunBlockingLensesVotesOneIsToday: THIRD_PARTY_VOTES boş/1 (test
// config'inde 0) -> eski tek-çağrı yolu: 1 çağrı, kayıtta "oy" öneki YOK,
// bugünkü blok/gerekçe.
func TestRunBlockingLensesVotesOneIsToday(t *testing.T) {
	for _, votes := range []int{0, 1} {
		set := newLensSet("", votes)
		chat := newScriptedLensChat(map[string][]string{lensThirdPartySystem: {"fail", "fail", "fail"}})

		outcome, verdicts := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

		if !outcome.Blocked || outcome.Check != lensThirdPartyName || outcome.Reason != "r1" {
			t.Errorf("votes=%d: tek fail bloklamalı: %+v", votes, outcome)
		}
		if chat.calls[lensThirdPartySystem] != 1 {
			t.Errorf("votes=%d: tek çağrı beklenirdi, geldi %d", votes, chat.calls[lensThirdPartySystem])
		}
		if len(verdicts) != 1 || len(outcome.Verdicts) != 1 {
			t.Fatalf("votes=%d: 1 karar/1 kayıt beklenirdi: %+v %+v", votes, verdicts, outcome.Verdicts)
		}
		if got := outcome.Verdicts[0]; got.Reason != "r1" || got.PromptVersion != "v1" {
			t.Errorf("votes=%d: kayıt önek-SİZ ve v1 olmalı: %+v", votes, got)
		}
	}
}

// TestRunBlockingLensesThirdPartyV3 (#197): THIRD_PARTY_PROMPT=v3 -> oylar v3
// sistem prompt'uyla çağrılır (v1 prompt'u HİÇ), kalıcı kayıt prompt_version
// "v3" taşır; veri-erişimi kaydı v1 kalır.
func TestRunBlockingLensesThirdPartyV3(t *testing.T) {
	set := newLensSet("v3", 2)
	chat := newScriptedLensChat(map[string][]string{
		lensThirdPartySystemV3: {"fail", "fail"},
	})

	outcome, _ := runBlockingLenses(context.Background(), chat, set.organic, "prompt", true, "card")

	if !outcome.Blocked || outcome.Check != lensThirdPartyName {
		t.Fatalf("v3 ile de oybirliği bloklamalı: %+v", outcome)
	}
	if chat.calls[lensThirdPartySystemV3] != 2 || chat.calls[lensThirdPartySystem] != 0 {
		t.Errorf("v3 prompt'u 2 kez, v1 prompt'u HİÇ çağrılmalı: %v", chat.calls)
	}
	for _, v := range outcome.Verdicts {
		if v.PromptVersion != "v3" {
			t.Errorf("kayıt prompt_version=v3 olmalı: %+v", v)
		}
	}

	// ayrışmadan sonra çağrılan veri-erişimi v1 kalır.
	chat2 := newScriptedLensChat(map[string][]string{lensThirdPartySystemV3: {"fail", "pass"}})
	outcome2, _ := runBlockingLenses(context.Background(), chat2, set.organic, "prompt", true, "card")
	last := outcome2.Verdicts[len(outcome2.Verdicts)-1]
	if last.Lens != "veri-erişimi" || last.PromptVersion != lensDataAccessVersion {
		t.Errorf("veri-erişimi kaydı v1 kalmalı: %+v", last)
	}
}

// TestLensVoteVerdictsFormat: kalıcı kayıt biçimi — n>1 iken önek (yapılan oy
// sayısından bağımsız), hata oyu "error", n==1'de önek yok.
func TestLensVoteVerdictsFormat(t *testing.T) {
	lens := seedLens{name: "x", version: "vX"}
	votes := []lensVoteRecord{
		{Verdict: lensVerdict{Verdict: "fail", Reason: "a"}, Model: "m1"},
		{Err: errors.New("boom"), Model: "m2"},
	}
	got := lensVoteVerdicts(lens, "seed", 3, votes)
	want := []store.LensVerdict{
		{Lens: "x", PromptVersion: "vX", Subject: "seed", Verdict: "fail", Reason: "oy 1/3: a", Model: "m1"},
		{Lens: "x", PromptVersion: "vX", Subject: "seed", Verdict: "error", Reason: "oy 2/3: boom", Model: "m2"},
	}
	for i := range want {
		got[i].At = want[i].At // zaman damgası karşılaştırma dışı
		if got[i] != want[i] {
			t.Errorf("kayıt %d: %+v beklenirdi, geldi %+v", i, want[i], got[i])
		}
	}
	single := lensVoteVerdicts(lens, "card", 1, votes[:1])
	if single[0].Reason != "a" {
		t.Errorf("n==1'de önek OLMAMALI: %q", single[0].Reason)
	}
}
