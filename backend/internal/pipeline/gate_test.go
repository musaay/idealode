package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/store"
)

// gateSeqChat, gate.go birim testleri için seedLenses SIRASINA göre
// SIRAYLA verdict/hata döner (her çağrıda listedeki bir sonraki eleman).
// errAt: -1 ise hiçbir çağrı hata vermez; aksi halde o sıradaki çağrı hata
// döner (kalanlar hiç çağrılmaz — erken çıkış doğrulaması, lensSeqChat'in
// synthesize_test.go'daki deseniyle AYNI).
type gateSeqChat struct {
	verdicts []string
	errAt    int

	calls int
}

func (c *gateSeqChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *gateSeqChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	idx := c.calls
	c.calls++
	if c.errAt >= 0 && idx == c.errAt {
		return "", fmt.Errorf("simulated mercek hatası")
	}
	v := "pass"
	if idx < len(c.verdicts) {
		v = c.verdicts[idx]
	}
	return fmt.Sprintf(`{"verdict":%q,"reason":"test-reason"}`, v), nil
}

// TestRunBlockingLensesStopOnFirstFailStopsEarly: stopOnFirstFail=true
// modunda ilk "fail"de durulduğunu (kalan mercekler ÇAĞRILMAZ) çağrı
// sayısıyla doğrular (#123'ün organik davranışı). #169: seedLenses artık
// 2 mercek (üçüncü-taraf, veri-erişimi) — ilk mercek fail döner, ikincisi
// (son mercek) hiç çağrılmaz.
func TestRunBlockingLensesStopOnFirstFailStopsEarly(t *testing.T) {
	chat := &gateSeqChat{verdicts: []string{"fail", "pass"}, errAt: -1}
	outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", true, "card")

	if !outcome.Blocked {
		t.Fatal("ilk mercek fail dönünce Blocked=true olmalı")
	}
	if outcome.Check != seedLenses[0].name {
		t.Errorf("bloklayan mercek adı %q beklenirdi, geldi %q", seedLenses[0].name, outcome.Check)
	}
	if chat.calls != 1 {
		t.Errorf("ilk fail'de erken çıkış: 1 çağrı beklenirdi (2.'si çağrılmamalı), geldi %d", chat.calls)
	}
	if len(verdicts) != 1 {
		t.Errorf("verdicts fail'e kadar (1 eleman) dönmeli, geldi %d", len(verdicts))
	}
}

// TestRunBlockingLensesStopOnFirstFailFalseRunsAll: stopOnFirstFail=false
// modunda TÜM mercekler çağrıldığını ve birden fazla "fail" varsa
// isim/sebeplerin birleştirildiğini doğrular (seeds.go'nun eski davranışı).
// #169: seedLenses 2 mercek olduğundan "birden fazla fail" senaryosu ikisinin
// de fail dönmesiyle kurulur.
func TestRunBlockingLensesStopOnFirstFailFalseRunsAll(t *testing.T) {
	chat := &gateSeqChat{verdicts: []string{"fail", "fail"}, errAt: -1}
	outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", false, "seed")

	if chat.calls != 2 {
		t.Fatalf("stopOnFirstFail=false TÜM mercekleri çağırmalı, 2 çağrı beklenirdi, geldi %d", chat.calls)
	}
	if len(verdicts) != 2 {
		t.Fatalf("verdicts tüm mercekleri içermeli, geldi %d", len(verdicts))
	}
	if !outcome.Blocked {
		t.Fatal("iki mercek de fail dönünce Blocked=true olmalı (fail baskın)")
	}
	wantCheck := seedLenses[0].name + ", " + seedLenses[1].name
	if outcome.Check != wantCheck {
		t.Errorf("birleştirilmiş mercek isimleri %q beklenirdi, geldi %q", wantCheck, outcome.Check)
	}
	wantReason := "test-reason; test-reason"
	if outcome.Reason != wantReason {
		t.Errorf("birleştirilmiş sebepler %q beklenirdi, geldi %q", wantReason, outcome.Reason)
	}
}

// TestRunBlockingLensesErrorStopsEarlyBothModes: mercek çağrısı HATA
// verirse (ağ/kota) İKİ modda da ilk hatada durulduğunu, outcome.Err
// dolduğunu ve Blocked=false kaldığını doğrular (bloklama YOK ilkesi).
// #169: ilk (index 0) çağrı hata verir, ikinci (son) mercek hiç çağrılmaz.
func TestRunBlockingLensesErrorStopsEarlyBothModes(t *testing.T) {
	for _, stopOnFirstFail := range []bool{true, false} {
		chat := &gateSeqChat{errAt: 0}
		outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", stopOnFirstFail, "card")

		if outcome.Err == nil {
			t.Errorf("stopOnFirstFail=%v: outcome.Err dolu olmalı", stopOnFirstFail)
		}
		if outcome.Blocked {
			t.Errorf("stopOnFirstFail=%v: mercek hatası BLOKLAMAMALI", stopOnFirstFail)
		}
		if outcome.Check != seedLenses[0].name {
			t.Errorf("stopOnFirstFail=%v: hata veren mercek adı %q beklenirdi, geldi %q", stopOnFirstFail, seedLenses[0].name, outcome.Check)
		}
		if chat.calls != 1 {
			t.Errorf("stopOnFirstFail=%v: hatada durulmalı, 1 çağrı beklenirdi (2.'si çağrılmamalı), geldi %d", stopOnFirstFail, chat.calls)
		}
		if len(verdicts) != 0 {
			t.Errorf("stopOnFirstFail=%v: verdicts hataya kadar (0 eleman) dönmeli, geldi %d", stopOnFirstFail, len(verdicts))
		}
	}
}

// TestMarketViabilityLensRemoved (#169, PO kararı 2026-09-25): pazar-
// işlerliği merceği, lead'in altın set ölçümünde (oybirliği, 3 koşu) eşsiz
// katkısı 0 çıktığından seedLenses/trendingLenses'ten KALDIRILDI — organik
// yol (synthesize.go, seedLenses'i kullanır) ve tohum yolu (seeds.go,
// seedLenses/trendingLenses) artık lensMarketViabilitySystem'i HİÇ ÇAĞIRMAZ;
// bu iki listenin dışında hiçbir çağrı yeri yok, dolayısıyla liste kontrolü
// çağrının yapılmadığını kanıtlar. lensMarketViabilitySystem/
// lensMarketViabilityVersion (ve lens_prompts_v3.go'daki V3 sabiti)
// KALDI — lens-ab registry'si ("market_viability") bunları hâlâ kullanıyor.
func TestMarketViabilityLensRemoved(t *testing.T) {
	for _, l := range seedLenses {
		if l.system == lensMarketViabilitySystem {
			t.Error("seedLenses artık pazar-işlerliği merceğini İÇERMEMELİ (#169)")
		}
		if l.name == "pazar-işlerliği" {
			t.Error("seedLenses'te 'pazar-işlerliği' adlı mercek KALMAMALI (#169)")
		}
	}
	for _, l := range trendingLenses {
		if l.system == lensMarketViabilitySystem {
			t.Error("trendingLenses artık pazar-işlerliği merceğini İÇERMEMELİ (#169)")
		}
		if l.name == "pazar-işlerliği" {
			t.Error("trendingLenses'te 'pazar-işlerliği' adlı mercek KALMAMALI (#169)")
		}
	}
	if len(seedLenses) != 2 {
		t.Errorf("seedLenses 2 mercek içermeli (üçüncü-taraf, veri-erişimi), geldi %d: %+v", len(seedLenses), seedLenses)
	}
	if len(trendingLenses) != 3 {
		t.Errorf("trendingLenses 3 mercek içermeli (ürünleştirilebilirlik + 2), geldi %d: %+v", len(trendingLenses), trendingLenses)
	}
}

// distinctChat, evaluateDistinctiveness birim testleri için sabit
// verdict/criterion/reason döner ya da hata verir. calls: kaç kez
// çağrıldığı (#166 yedek/düşüş testlerinde hangi istemcinin çağrıldığını
// saymak için).
type distinctChat struct {
	verdict, criterion string
	err                bool
	calls              int
}

func (c *distinctChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *distinctChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.calls++
	if c.err {
		return "", errors.New("simulated özgünlük merceği hatası")
	}
	return fmt.Sprintf(`{"verdict":%q,"criterion":%q,"reason":"test-reason"}`, c.verdict, c.criterion), nil
}

// namedDistinctChat, distinctChat'in llm.NamedChat uygulayan hâli (#166) —
// hangi istemcinin karar verdiğinin store.LensVerdict.Model alanına doğru
// yazıldığını sınamak için.
type namedDistinctChat struct {
	distinctChat
	model string
}

func (c *namedDistinctChat) ModelName() string { return c.model }

// TestEvaluateDistinctivenessK1Blocks: K1 (doygunluk) "fail"i Blocked=true
// + Stage=distinctiveness + Criterion=K1 döndürmeli.
func TestEvaluateDistinctivenessK1Blocks(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K1"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 1)

	if !outcome.Blocked {
		t.Error("K1 fail Blocked=true döndürmeli")
	}
	if outcome.Stage != "distinctiveness" {
		t.Errorf("Stage=distinctiveness beklenirdi, geldi %q", outcome.Stage)
	}
	if outcome.Criterion != "K1" {
		t.Errorf("Criterion=K1 beklenirdi, geldi %q", outcome.Criterion)
	}
	if outcome.Err != nil {
		t.Errorf("K1 blokta Err nil olmalı, geldi %v", outcome.Err)
	}
}

// TestEvaluateDistinctivenessK2Blocks: K2 (yerleşik çözüm) "fail"i de K1
// gibi Blocked=true + Stage=distinctiveness + Criterion=K2 döndürmeli
// (#166: K2 artık K1 ile AYNI şekilde bloklayıcı).
func TestEvaluateDistinctivenessK2Blocks(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K2"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 1)

	if !outcome.Blocked {
		t.Error("K2 fail Blocked=true döndürmeli (#166)")
	}
	if outcome.Stage != "distinctiveness" {
		t.Errorf("Stage=distinctiveness beklenirdi, geldi %q", outcome.Stage)
	}
	if outcome.Criterion != "K2" {
		t.Errorf("Criterion=K2 beklenirdi, geldi %q", outcome.Criterion)
	}
	if outcome.Err != nil {
		t.Errorf("K2 blokta Err nil olmalı, geldi %v", outcome.Err)
	}
}

// TestEvaluateDistinctivenessK3RecordsButDoesNotBlock: K3-K4 (örn. K3)
// "fail"i KAYIT için Stage=distinctiveness döndürmeli ama Blocked=false
// olmalı (kart yine yazılır, #138, #166).
func TestEvaluateDistinctivenessK3RecordsButDoesNotBlock(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K3"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 1)

	if outcome.Blocked {
		t.Error("K3 fail Blocked=false olmalı (bloklamayan kriter)")
	}
	if outcome.Stage != "distinctiveness" {
		t.Errorf("Stage=distinctiveness beklenirdi (kayıt için), geldi %q", outcome.Stage)
	}
	if outcome.Criterion != "K3" {
		t.Errorf("Criterion=K3 beklenirdi, geldi %q", outcome.Criterion)
	}
}

// TestEvaluateDistinctivenessK4RecordsButDoesNotBlock: K4 (kırılganlık)
// "fail"i de K3 gibi yalnız KAYIT için Stage=distinctiveness döndürmeli,
// Blocked=false kalmalı (#166: K3/K4 hâlâ yalnız işaret).
func TestEvaluateDistinctivenessK4RecordsButDoesNotBlock(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K4"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 1)

	if outcome.Blocked {
		t.Error("K4 fail Blocked=false olmalı (bloklamayan kriter)")
	}
	if outcome.Stage != "distinctiveness" {
		t.Errorf("Stage=distinctiveness beklenirdi (kayıt için), geldi %q", outcome.Stage)
	}
	if outcome.Criterion != "K4" {
		t.Errorf("Criterion=K4 beklenirdi, geldi %q", outcome.Criterion)
	}
}

// TestEvaluateDistinctivenessErrorPasses: mercek çağrısı HATA verirse
// Stage="" (kayıt yok) ve Blocked=false — bloklama YOK ilkesi hata
// durumunda da geçerli (#101 v3 edge case).
func TestEvaluateDistinctivenessErrorPasses(t *testing.T) {
	chat := &distinctChat{err: true}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 1)

	if outcome.Err == nil {
		t.Fatal("outcome.Err dolu olmalı")
	}
	if outcome.Blocked {
		t.Error("mercek hatası BLOKLAMAMALI")
	}
	if outcome.Stage != "" {
		t.Errorf("hata durumunda Stage boş olmalı (kayıt yok), geldi %q", outcome.Stage)
	}
	if idea.DistinctivenessVerdict != nil {
		t.Errorf("mercek hatasında distinctiveness_verdict NULL kalmalı, geldi %v", *idea.DistinctivenessVerdict)
	}
}

// TestEvaluateDistinctivenessUsesDistinctChatWhenProvided (#166): ayrı bir
// özgünlük istemcisi (distinctChat) verilmişse ÖNCE o kullanılır, başarılı
// olursa varsayılan istemci (chat) HİÇ çağrılmaz ve kalıcı kayıttaki Model
// alanı ayrı istemcinin adını taşır.
func TestEvaluateDistinctivenessUsesDistinctChatWhenProvided(t *testing.T) {
	def := &namedDistinctChat{distinctChat: distinctChat{verdict: "pass", criterion: "none"}, model: "default-model"}
	distinct := &namedDistinctChat{distinctChat: distinctChat{verdict: "fail", criterion: "K1"}, model: "gemini-3.5-flash-lite"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), def, idea, 1, distinct)

	if !outcome.Blocked {
		t.Fatal("ayrı istemcinin K1 fail kararı bloklamalı (varsayılan çağrılsaydı pass dönerdi)")
	}
	if distinct.calls != 1 {
		t.Errorf("ayrı istemci tam 1 kez çağrılmalı, geldi %d", distinct.calls)
	}
	if def.calls != 0 {
		t.Errorf("ayrı istemci başarılıyken varsayılan istemci HİÇ çağrılmamalı, geldi %d çağrı", def.calls)
	}
	if len(outcome.Verdicts) != 1 || outcome.Verdicts[0].Model != "gemini-3.5-flash-lite" {
		t.Errorf("verdict.Model ayrı istemcinin modeli olmalı, geldi: %+v", outcome.Verdicts)
	}
}

// TestEvaluateDistinctivenessFallsBackToDefaultOnDistinctChatError (#166):
// ayrı istemci hata verirse AYNI çağrı bir kez varsayılan istemciyle
// tekrar denenir; varsayılan başarılı olursa onun kararı ve model adı
// kullanılır.
func TestEvaluateDistinctivenessFallsBackToDefaultOnDistinctChatError(t *testing.T) {
	def := &namedDistinctChat{distinctChat: distinctChat{verdict: "fail", criterion: "K2"}, model: "default-model"}
	distinct := &namedDistinctChat{distinctChat: distinctChat{err: true}, model: "gemini-3.5-flash-lite"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), def, idea, 1, distinct)

	if outcome.Err != nil {
		t.Fatalf("yedek deneme başarılıyken outcome.Err nil olmalı, geldi: %v", outcome.Err)
	}
	if distinct.calls != 1 {
		t.Errorf("ayrı istemci tam 1 kez çağrılmalı (hata sonrası tekrar denenmez), geldi %d", distinct.calls)
	}
	if def.calls != 1 {
		t.Errorf("varsayılan istemci tam 1 kez (yedek olarak) çağrılmalı, geldi %d", def.calls)
	}
	if !outcome.Blocked || outcome.Criterion != "K2" {
		t.Errorf("varsayılanın K2 fail kararı bloklamalı, geldi Blocked=%v Criterion=%q", outcome.Blocked, outcome.Criterion)
	}
	if len(outcome.Verdicts) != 1 || outcome.Verdicts[0].Model != "default-model" {
		t.Errorf("verdict.Model yedeğe düşülünce varsayılanın modeli olmalı, geldi: %+v", outcome.Verdicts)
	}
}

// TestEvaluateDistinctivenessBothClientsErrorNoBlock (#166): ayrı istemci
// VE yedek (varsayılan) istemci de hata verirse bugünkü davranış aynen
// korunur — outcome.Err dolu, Blocked=false, Stage="" (kayıt yok).
func TestEvaluateDistinctivenessBothClientsErrorNoBlock(t *testing.T) {
	def := &namedDistinctChat{distinctChat: distinctChat{err: true}, model: "default-model"}
	distinct := &namedDistinctChat{distinctChat: distinctChat{err: true}, model: "gemini-3.5-flash-lite"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), def, idea, 1, distinct)

	if outcome.Err == nil {
		t.Fatal("ikisi de hata verince outcome.Err dolu olmalı")
	}
	if outcome.Blocked {
		t.Error("mercek hatası (ikisi de) BLOKLAMAMALI")
	}
	if outcome.Stage != "" {
		t.Errorf("hata durumunda Stage boş olmalı (kayıt yok), geldi %q", outcome.Stage)
	}
	if distinct.calls != 1 || def.calls != 1 {
		t.Errorf("ikisi de tam 1'er kez çağrılmalı, geldi distinct=%d default=%d", distinct.calls, def.calls)
	}
	if idea.DistinctivenessVerdict != nil {
		t.Errorf("ikisi de hatada distinctiveness_verdict NULL kalmalı, geldi %v", *idea.DistinctivenessVerdict)
	}
}

// TestEvaluateDistinctivenessSkipsFallbackWhenContextCancelled (#166 küçük
// düzeltme): ctx zaten iptal edildiyse (ör. koşu deadline/cancel) ayrı
// istemci hata verse bile varsayılana yedek deneme YAPILMAZ — boşuna ikinci
// bir HTTP denemesi yaratılmaz.
func TestEvaluateDistinctivenessSkipsFallbackWhenContextCancelled(t *testing.T) {
	def := &namedDistinctChat{distinctChat: distinctChat{verdict: "pass", criterion: "none"}, model: "default-model"}
	distinct := &namedDistinctChat{distinctChat: distinctChat{err: true}, model: "gemini-3.5-flash-lite"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := evaluateDistinctiveness(ctx, def, idea, 1, distinct)

	if outcome.Err == nil {
		t.Fatal("ayrı istemci hatası ctx iptaliyle birlikte outcome.Err'e yansımalı")
	}
	if distinct.calls != 1 {
		t.Errorf("ayrı istemci tam 1 kez çağrılmalı, geldi %d", distinct.calls)
	}
	if def.calls != 0 {
		t.Errorf("ctx iptal edildiyse varsayılan istemci HİÇ çağrılmamalı, geldi %d çağrı", def.calls)
	}
}

// TestApplyGateOutcomeHoldOnlyCalledWhenBlocked: hold() yalnız
// o.Blocked==true iken çağrılmalı; recordElimination o.Stage!="" iken HER
// ZAMAN çağrılmalı (K3-K4 dahil) — best-effort, hold bağımsız.
func TestApplyGateOutcomeHoldOnlyCalledWhenBlocked(t *testing.T) {
	origWrite := writeElimination
	t.Cleanup(func() { writeElimination = origWrite })

	cases := []struct {
		name       string
		outcome    gateOutcome
		wantRecord bool
		wantHold   bool
	}{
		{"blocked -> record + hold", gateOutcome{Blocked: true, Stage: "blocking_lens", Check: "x", Reason: "r"}, true, true},
		{"not-blocked ama Stage dolu (K3 gibi) -> record, hold YOK", gateOutcome{Blocked: false, Stage: "distinctiveness", Criterion: "K3", Reason: "r"}, true, false},
		{"Stage boş (pass/unsure/hata) -> ne record ne hold", gateOutcome{Blocked: false, Stage: ""}, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recorded := false
			writeElimination = func(ctx context.Context, st *store.Store, e store.Elimination) (int64, error) {
				recorded = true
				return 1, nil
			}
			held := false
			hold := func() error { held = true; return nil }

			if err := applyGateOutcome(context.Background(), nil, c.outcome, "subject", "detail", hold); err != nil {
				t.Fatalf("applyGateOutcome: %v", err)
			}
			if recorded != c.wantRecord {
				t.Errorf("recordElimination çağrısı: beklenen %v, geldi %v", c.wantRecord, recorded)
			}
			if held != c.wantHold {
				t.Errorf("hold() çağrısı: beklenen %v, geldi %v", c.wantHold, held)
			}
		})
	}
}

// TestApplyGateOutcomeReturnsHoldError: hold() hata dönerse
// applyGateOutcome o hatayı ÇAĞIRANA döner (organik/tohum yolunun kendi
// hata tutumunu uygulayabilmesi için).
func TestApplyGateOutcomeReturnsHoldError(t *testing.T) {
	origWrite := writeElimination
	writeElimination = func(ctx context.Context, st *store.Store, e store.Elimination) (int64, error) { return 1, nil }
	t.Cleanup(func() { writeElimination = origWrite })

	wantErr := errors.New("simulated hold hatası")
	hold := func() error { return wantErr }

	err := applyGateOutcome(context.Background(), nil, gateOutcome{Blocked: true, Stage: "blocking_lens"}, "s", "d", hold)
	if !errors.Is(err, wantErr) {
		t.Errorf("hold hatası aynen dönmeli, geldi: %v", err)
	}
}

// ---------------------------------------------------------------------
// voteDistinctiveness (#181, PO kararı 2026-09-23 "oybirliğiyle blok") —
// SAF çekirdeğin tablo testleri. seqVoteCall, önceden tanımlı bir
// (verdict,criterion,reason) dizisini SIRAYLA döner; errAt (-1 = hiç hata
// yok) o INDEXTEKİ (0-tabanlı) çağrıda hata döner.

func seqVoteCall(votes []lensVerdict, errAt int) (voteDistinctivenessCall, *int) {
	calls := new(int)
	fn := func(ctx context.Context) (lensVerdict, string, error) {
		idx := *calls
		*calls++
		if errAt >= 0 && idx == errAt {
			return lensVerdict{}, "m", errors.New("simulated oy hatası")
		}
		if idx < len(votes) {
			return votes[idx], "m", nil
		}
		return lensVerdict{Verdict: "pass", Criterion: "none", Reason: "fazla çağrı"}, "m", nil
	}
	return fn, calls
}

// TestVoteDistinctivenessN1IdenticalToToday: N=1 (üretim varsayılanı) her
// verdict/kriter için BUGÜNKÜ tek-çağrı davranışıyla birebir olmalı — 1
// çağrı, aynı alanlar, aynı blok kararı.
func TestVoteDistinctivenessN1IdenticalToToday(t *testing.T) {
	cases := []struct {
		name        string
		v           lensVerdict
		wantBlocked bool
	}{
		{"pass", lensVerdict{Verdict: "pass", Criterion: "none", Reason: "temiz"}, false},
		{"fail K1 (blok)", lensVerdict{Verdict: "fail", Criterion: "K1", Reason: "doygun"}, true},
		{"fail K2 (blok)", lensVerdict{Verdict: "fail", Criterion: "K2", Reason: "yerleşik"}, true},
		{"fail K3 (blok değil, kayıt)", lensVerdict{Verdict: "fail", Criterion: "K3", Reason: "talep yok"}, false},
		{"unsure", lensVerdict{Verdict: "unsure", Criterion: "none", Reason: "emin değilim"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, calls := seqVoteCall([]lensVerdict{tc.v}, -1)
			decision, votes := voteDistinctiveness(context.Background(), 1, call)
			if *calls != 1 {
				t.Errorf("N=1: 1 çağrı beklenirdi, geldi %d", *calls)
			}
			if decision.Err != nil {
				t.Fatalf("N=1: Err olmamalı, geldi %v", decision.Err)
			}
			if decision.Disputed {
				t.Error("N=1: Disputed asla true olmamalı (tartışma için en az 2 oy gerekir)")
			}
			if decision.Blocked != tc.wantBlocked {
				t.Errorf("Blocked=%v beklenirdi, geldi %v", tc.wantBlocked, decision.Blocked)
			}
			if decision.Verdict != tc.v.Verdict || decision.Criterion != tc.v.Criterion || decision.Reason != tc.v.Reason {
				t.Errorf("N=1 alanlar bugünküyle BİREBİR olmalı: istenen %+v, geldi %+v", tc.v, decision)
			}
			if len(votes) != 1 {
				t.Fatalf("1 oy kaydı beklenirdi, geldi %d", len(votes))
			}
		})
	}
}

// TestVoteDistinctivenessN3PassStopsAtFirstVote: N=3'te ilk oy pass (blok
// DEĞİL) ise tek çağrıda durulur, blok yok, tartışma yok.
func TestVoteDistinctivenessN3PassStopsAtFirstVote(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{{Verdict: "pass", Criterion: "none", Reason: "temiz"}}, -1)
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 1 {
		t.Errorf("pass ilk oyda durmalı: 1 çağrı beklenirdi, geldi %d", *calls)
	}
	if decision.Blocked || decision.Disputed {
		t.Errorf("pass blok/tartışmalı OLMAMALI: %+v", decision)
	}
	if decision.Verdict != "pass" {
		t.Errorf("Verdict=pass beklenirdi, geldi %q", decision.Verdict)
	}
	if len(votes) != 1 {
		t.Errorf("1 oy kaydı beklenirdi, geldi %d", len(votes))
	}
}

// TestVoteDistinctivenessBlockThenPassIsDisputed: blok,pass -> 2 çağrı,
// TARTIŞMALI (unsure + K1 + "tartışmalı: 1/2 oy blok — ..." gerekçesi),
// blok YOK.
func TestVoteDistinctivenessBlockThenPassIsDisputed(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "doygun"},
		{Verdict: "pass", Criterion: "none", Reason: "temiz"},
	}, -1)
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 2 {
		t.Fatalf("2 çağrı beklenirdi (3.'sü çağrılmamalı), geldi %d", *calls)
	}
	if decision.Blocked {
		t.Error("tartışmalı durumda Blocked=false olmalı")
	}
	if !decision.Disputed {
		t.Fatal("Disputed=true olmalı")
	}
	if decision.Verdict != "unsure" || decision.Criterion != "K1" {
		t.Errorf("tartışmalı karar yanlış: %+v", decision)
	}
	wantReason := "tartışmalı: 1/2 oy blok — doygun"
	if decision.Reason != wantReason {
		t.Errorf("tartışmalı gerekçe: %q beklenirdi, geldi %q", wantReason, decision.Reason)
	}
	if len(votes) != 2 {
		t.Errorf("2 oy kaydı beklenirdi, geldi %d", len(votes))
	}
}

// TestVoteDistinctivenessAllBlockBlocks: blok,blok,blok -> 3 çağrı, BLOKLA
// — Criterion/Reason İLK oyunkiler.
func TestVoteDistinctivenessAllBlockBlocks(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "ilk gerekçe"},
		{Verdict: "fail", Criterion: "K1", Reason: "ikinci gerekçe"},
		{Verdict: "fail", Criterion: "K1", Reason: "üçüncü gerekçe"},
	}, -1)
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 3 {
		t.Fatalf("3 çağrı beklenirdi, geldi %d", *calls)
	}
	if !decision.Blocked || decision.Disputed {
		t.Errorf("TÜM oylar blokken Blocked=true Disputed=false olmalı: %+v", decision)
	}
	if decision.Verdict != "fail" || decision.Criterion != "K1" {
		t.Errorf("Verdict/Criterion yanlış: %+v", decision)
	}
	if decision.Reason != "ilk gerekçe" {
		t.Errorf("Reason İLK oyunki olmalı, istenen %q geldi %q", "ilk gerekçe", decision.Reason)
	}
	if len(votes) != 3 {
		t.Errorf("3 oy kaydı beklenirdi, geldi %d", len(votes))
	}
}

// TestVoteDistinctivenessBlockBlockFailK3IsDisputed: blok,blok,fail-K3 ->
// TARTIŞMALI (K3 blok oyu SAYILMAZ, üçüncü oy "blok değil" sayılır).
func TestVoteDistinctivenessBlockBlockFailK3IsDisputed(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "ilk"},
		{Verdict: "fail", Criterion: "K2", Reason: "ikinci"},
		{Verdict: "fail", Criterion: "K3", Reason: "üçüncü"},
	}, -1)
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 3 {
		t.Fatalf("3 çağrı beklenirdi, geldi %d", *calls)
	}
	if decision.Blocked || !decision.Disputed {
		t.Errorf("K3 blok OYU DEĞİL, tartışmalı olmalı: %+v", decision)
	}
	if decision.Criterion != "K1" {
		t.Errorf("kriter İLK blok oyunun (K1) olmalı, geldi %q", decision.Criterion)
	}
	if len(votes) != 3 {
		t.Errorf("3 oy kaydı beklenirdi, geldi %d", len(votes))
	}
}

// TestVoteDistinctivenessK2CountsAsBlock: K2 (yerleşik çözüm) de K1 gibi
// blok oyu SAYILIR (#166 ile AYNI ikili).
func TestVoteDistinctivenessK2CountsAsBlock(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{{Verdict: "fail", Criterion: "K2", Reason: "yerleşik"}}, -1)
	decision, _ := voteDistinctiveness(context.Background(), 1, call)
	if *calls != 1 || !decision.Blocked || decision.Criterion != "K2" {
		t.Errorf("K2 tek oyla (N=1) bloklamalı: çağrı=%d karar=%+v", *calls, decision)
	}
}

// TestVoteDistinctivenessFirstVoteErrorReturnsErr: k==1'İN KENDİSİ hata
// verirse bugünkü tek-çağrı davranışı birebir — Decision.Err dolu, kalan
// oylar ÇAĞRILMAZ.
func TestVoteDistinctivenessFirstVoteErrorReturnsErr(t *testing.T) {
	call, calls := seqVoteCall(nil, 0)
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 1 {
		t.Fatalf("k=1 hatasında durulmalı, 1 çağrı beklenirdi, geldi %d", *calls)
	}
	if decision.Err == nil {
		t.Fatal("k==1 hatasında Err dolu olmalı")
	}
	if decision.Blocked || decision.Disputed {
		t.Errorf("k==1 hatasında Blocked/Disputed olmamalı: %+v", decision)
	}
	if len(votes) != 1 || votes[0].Err == nil {
		t.Errorf("hata oyu kaydı beklenirdi: %+v", votes)
	}
}

// TestVoteDistinctivenessSecondVoteErrorIsDisputed: k>1 hatası (yedek
// denemeden sonra çağırana ulaşan hata) oybirliği kurulamadı sayılır —
// TARTIŞMALI (Err YOK, bloklama YOK), hata oyu da kayda girer.
func TestVoteDistinctivenessSecondVoteErrorIsDisputed(t *testing.T) {
	call, calls := seqVoteCall([]lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "ilk"},
	}, 1) // idx=1 (2. çağrı) hata verir
	decision, votes := voteDistinctiveness(context.Background(), 3, call)
	if *calls != 2 {
		t.Fatalf("2 çağrı beklenirdi (3.'sü çağrılmamalı), geldi %d", *calls)
	}
	if decision.Err != nil {
		t.Errorf("k>1 hatasında Err DOLU OLMAMALI (tartışmalı sayılır), geldi %v", decision.Err)
	}
	if decision.Blocked || !decision.Disputed {
		t.Errorf("k>1 hatasında tartışmalı olmalı: %+v", decision)
	}
	if decision.Verdict != "unsure" || decision.Criterion != "K1" {
		t.Errorf("tartışmalı karar alanları yanlış: %+v", decision)
	}
	if len(votes) != 2 || votes[1].Err == nil {
		t.Errorf("hata oyu kaydı beklenirdi: %+v", votes)
	}
}

// ---------------------------------------------------------------------
// evaluateDistinctiveness'in N-oy KABLOLAMASI (call closure + kayıt +
// idea alanları) — voteDistinctiveness'in kendisi yukarıda AYRI sınandı.

// seqDistinctChat, evaluateDistinctiveness'in N-oy testleri için SIRAYLA
// farklı verdict/criterion/reason döner (üstteki distinctChat'in AKSİNE
// SABİT değil, dizi bazlı — gerçek bir llm.Chat üzerinden voteDistinctiveness
// entegrasyonunu sınamak için).
type seqDistinctChat struct {
	votes []lensVerdict
	idx   int
}

func (c *seqDistinctChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *seqDistinctChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	v := c.votes[c.idx]
	c.idx++
	return fmt.Sprintf(`{"verdict":%q,"criterion":%q,"reason":%q}`, v.Verdict, v.Criterion, v.Reason), nil
}

// TestEvaluateDistinctivenessVotesDisputedWritesUnsure: N=3, blok+pass ->
// oylar ayrışır; idea alanları "unsure"+ilk blok kriteri ile YAZILIR
// (kart bloklanmaz), gateOutcome.Stage="" (eliminations'a KAYIT YOK),
// Disputed=true.
func TestEvaluateDistinctivenessVotesDisputedWritesUnsure(t *testing.T) {
	chat := &seqDistinctChat{votes: []lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "doygun"},
		{Verdict: "pass", Criterion: "none", Reason: "temiz"},
	}}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 3)

	if outcome.Blocked {
		t.Error("tartışmalı kart bloklanmamalı")
	}
	if outcome.Stage != "" {
		t.Errorf("tartışmalıda Stage boş olmalı (eliminations'a YAZILMAZ), geldi %q", outcome.Stage)
	}
	if !outcome.Disputed {
		t.Error("outcome.Disputed=true olmalı")
	}
	if idea.DistinctivenessVerdict == nil || *idea.DistinctivenessVerdict != "unsure" {
		t.Errorf("kart alanı verdict=unsure olmalı, geldi %v", idea.DistinctivenessVerdict)
	}
	if idea.DistinctivenessCriterion == nil || *idea.DistinctivenessCriterion != "K1" {
		t.Errorf("kart alanı criterion=K1 (ilk blok oyu) olmalı, geldi %v", idea.DistinctivenessCriterion)
	}
	if len(outcome.Verdicts) != 2 {
		t.Fatalf("2 store.LensVerdict kaydı beklenirdi, geldi %d", len(outcome.Verdicts))
	}
	for i, v := range outcome.Verdicts {
		wantPrefix := fmt.Sprintf("oy %d/3: ", i+1)
		if !strings.HasPrefix(v.Reason, wantPrefix) {
			t.Errorf("kayıt %d %q öneki taşımalı, geldi %q", i, wantPrefix, v.Reason)
		}
	}
}

// TestEvaluateDistinctivenessVotesAllBlockBlocks: N=3, blok,blok,blok ->
// kart bloklanır, gateOutcome.Blocked=true, Stage="distinctiveness".
func TestEvaluateDistinctivenessVotesAllBlockBlocks(t *testing.T) {
	chat := &seqDistinctChat{votes: []lensVerdict{
		{Verdict: "fail", Criterion: "K1", Reason: "ilk"},
		{Verdict: "fail", Criterion: "K1", Reason: "ikinci"},
		{Verdict: "fail", Criterion: "K1", Reason: "üçüncü"},
	}}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea, 3)

	if !outcome.Blocked || outcome.Stage != "distinctiveness" || outcome.Criterion != "K1" {
		t.Errorf("N/N blokta Blocked=true Stage=distinctiveness Criterion=K1 beklenirdi: %+v", outcome)
	}
	if outcome.Disputed {
		t.Error("N/N blokta Disputed=false olmalı")
	}
	if len(outcome.Verdicts) != 3 {
		t.Errorf("3 store.LensVerdict kaydı beklenirdi, geldi %d", len(outcome.Verdicts))
	}
}

// TestEvaluateDistinctivenessVotesFallbackPerVote (#181): ayrı istemci
// (distinctChat) HER OYDA hata verirse, o oy İÇİN AYRI AYRI (tek seferlik)
// varsayılan istemciye yedek düşülür — "yedek istemci kuralı her oy için
// aynı".
func TestEvaluateDistinctivenessVotesFallbackPerVote(t *testing.T) {
	def := &namedDistinctChat{distinctChat: distinctChat{verdict: "fail", criterion: "K1"}, model: "default-model"}
	distinct := &namedDistinctChat{distinctChat: distinctChat{err: true}, model: "gemini-3.5-flash-lite"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), def, idea, 2, distinct)

	if distinct.calls != 2 {
		t.Errorf("ayrı istemci HER oyda 1 kez (toplam 2) denenmeli, geldi %d", distinct.calls)
	}
	if def.calls != 2 {
		t.Errorf("varsayılan istemci HER oyda yedek olarak 1 kez (toplam 2) çağrılmalı, geldi %d", def.calls)
	}
	if !outcome.Blocked {
		t.Errorf("iki oy da (yedekle) K1 fail dönünce bloklamalı: %+v", outcome)
	}
	if len(outcome.Verdicts) != 2 {
		t.Fatalf("2 store.LensVerdict kaydı beklenirdi, geldi %d", len(outcome.Verdicts))
	}
	for _, v := range outcome.Verdicts {
		if v.Model != "default-model" {
			t.Errorf("kayıttaki Model yedeğe düşülen istemcininki (default-model) olmalı, geldi %q", v.Model)
		}
	}
}
