package pipeline

import (
	"context"
	"errors"
	"fmt"
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
// sayısıyla doğrular (#123'ün organik davranışı).
func TestRunBlockingLensesStopOnFirstFailStopsEarly(t *testing.T) {
	chat := &gateSeqChat{verdicts: []string{"pass", "fail", "pass"}, errAt: -1}
	outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", true, "card")

	if !outcome.Blocked {
		t.Fatal("ikinci mercek fail dönünce Blocked=true olmalı")
	}
	if outcome.Check != seedLenses[1].name {
		t.Errorf("bloklayan mercek adı %q beklenirdi, geldi %q", seedLenses[1].name, outcome.Check)
	}
	if chat.calls != 2 {
		t.Errorf("ilk fail'de erken çıkış: 2 çağrı beklenirdi (3.'sü çağrılmamalı), geldi %d", chat.calls)
	}
	if len(verdicts) != 2 {
		t.Errorf("verdicts fail'e kadar (2 eleman) dönmeli, geldi %d", len(verdicts))
	}
}

// TestRunBlockingLensesStopOnFirstFailFalseRunsAll: stopOnFirstFail=false
// modunda TÜM mercekler çağrıldığını ve birden fazla "fail" varsa
// isim/sebeplerin birleştirildiğini doğrular (seeds.go'nun eski davranışı).
func TestRunBlockingLensesStopOnFirstFailFalseRunsAll(t *testing.T) {
	chat := &gateSeqChat{verdicts: []string{"fail", "unsure", "fail"}, errAt: -1}
	outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", false, "seed")

	if chat.calls != 3 {
		t.Fatalf("stopOnFirstFail=false TÜM mercekleri çağırmalı, 3 çağrı beklenirdi, geldi %d", chat.calls)
	}
	if len(verdicts) != 3 {
		t.Fatalf("verdicts tüm mercekleri içermeli, geldi %d", len(verdicts))
	}
	if !outcome.Blocked {
		t.Fatal("iki mercek fail dönünce Blocked=true olmalı (fail baskın)")
	}
	wantCheck := seedLenses[0].name + ", " + seedLenses[2].name
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
func TestRunBlockingLensesErrorStopsEarlyBothModes(t *testing.T) {
	for _, stopOnFirstFail := range []bool{true, false} {
		chat := &gateSeqChat{errAt: 1}
		outcome, verdicts := runBlockingLenses(context.Background(), chat, seedLenses, "prompt", stopOnFirstFail, "card")

		if outcome.Err == nil {
			t.Errorf("stopOnFirstFail=%v: outcome.Err dolu olmalı", stopOnFirstFail)
		}
		if outcome.Blocked {
			t.Errorf("stopOnFirstFail=%v: mercek hatası BLOKLAMAMALI", stopOnFirstFail)
		}
		if outcome.Check != seedLenses[1].name {
			t.Errorf("stopOnFirstFail=%v: hata veren mercek adı %q beklenirdi, geldi %q", stopOnFirstFail, seedLenses[1].name, outcome.Check)
		}
		if chat.calls != 2 {
			t.Errorf("stopOnFirstFail=%v: hatada durulmalı, 2 çağrı beklenirdi (3.'sü çağrılmamalı), geldi %d", stopOnFirstFail, chat.calls)
		}
		if len(verdicts) != 1 {
			t.Errorf("stopOnFirstFail=%v: verdicts hataya kadar (1 eleman) dönmeli, geldi %d", stopOnFirstFail, len(verdicts))
		}
	}
}

// distinctChat, evaluateDistinctiveness birim testleri için sabit
// verdict/criterion/reason döner ya da hata verir.
type distinctChat struct {
	verdict, criterion string
	err                bool
}

func (c *distinctChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *distinctChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	if c.err {
		return "", errors.New("simulated özgünlük merceği hatası")
	}
	return fmt.Sprintf(`{"verdict":%q,"criterion":%q,"reason":"test-reason"}`, c.verdict, c.criterion), nil
}

// TestEvaluateDistinctivenessK1Blocks: K1 (doygunluk) "fail"i Blocked=true
// + Stage=distinctiveness + Criterion=K1 döndürmeli.
func TestEvaluateDistinctivenessK1Blocks(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K1"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea)

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

// TestEvaluateDistinctivenessK3RecordsButDoesNotBlock: K2-K4 (örn. K3)
// "fail"i KAYIT için Stage=distinctiveness döndürmeli ama Blocked=false
// olmalı (kart yine yazılır, #138).
func TestEvaluateDistinctivenessK3RecordsButDoesNotBlock(t *testing.T) {
	chat := &distinctChat{verdict: "fail", criterion: "K3"}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea)

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

// TestEvaluateDistinctivenessErrorPasses: mercek çağrısı HATA verirse
// Stage="" (kayıt yok) ve Blocked=false — bloklama YOK ilkesi hata
// durumunda da geçerli (#101 v3 edge case).
func TestEvaluateDistinctivenessErrorPasses(t *testing.T) {
	chat := &distinctChat{err: true}
	idea := &store.Idea{Title: "X", ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u"}

	outcome := evaluateDistinctiveness(context.Background(), chat, idea)

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

// TestApplyGateOutcomeHoldOnlyCalledWhenBlocked: hold() yalnız
// o.Blocked==true iken çağrılmalı; recordElimination o.Stage!="" iken HER
// ZAMAN çağrılmalı (K2-K4 dahil) — best-effort, hold bağımsız.
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
