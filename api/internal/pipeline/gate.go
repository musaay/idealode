package pipeline

import (
	"context"
	"strings"

	"github.com/musaay/idealode/api/internal/llm"
	"github.com/musaay/idealode/api/internal/store"
)

// gate.go (#153): kart olup olmama kararını veren 4 kontrolün (3 bloklayıcı
// mercek: üçüncü-taraf/veri-erişimi/pazar-işlerliği + özgünlük merceği)
// SONUCUNU tek tipte taşıyan ve sonucuna göre yapılan yan etkileri (eleme
// kaydı, tema/tohum bekletme) TEK yerde uygulayan ortak katman. Organik yol
// (synthesize.go) ve tohum yolu (seeds.go) bilinçli olarak FARKLI davranır
// (mercek girdisi, zamanı, ilk fail'de durma politikası, hata sonrası
// tutum) — bu dosya o farkları PARAMETRE ile taşır, davranışı DEĞİŞTİRMEZ.

// gateOutcome, kart kapısındaki bir kontrolün (bloklayıcı mercek grubu ya da
// özgünlük merceği) sonucunu taşır.
type gateOutcome struct {
	// Blocked: true ise kart YAZILMAZ (organikte tema, tohumda tohum
	// bekletilir/mark'lanır — applyGateOutcome'daki hold()).
	Blocked bool
	// Stage: eliminations.stage değeri — "blocking_lens" ya da
	// "distinctiveness". Boş ("") ise KAYDEDİLECEK bir "fail" yok (pass/
	// unsure/mercek hatası) — applyGateOutcome hiçbir şey yapmaz.
	Stage string
	// Check: bloklayan/hata veren merceğin adı (organikte tek isim; tohum
	// yolunda birden fazla mercek "fail" dönerse ", " ile birleştirilmiş
	// isimler — mevcut log biçimiyle birebir).
	Check string
	// Criterion: yalnız Stage=="distinctiveness" iken K1..K4 dolu.
	Criterion string
	// Reason: bloklayan/fail sebebi (tohum yolunda birden fazla mercek
	// "fail" dönerse "; " ile birleştirilmiş sebepler).
	Reason string
	// Err: mercek/özgünlük ÇAĞRI hatası (ağ/kota) — bloklamaz, çağıran
	// loglar, kart/tohum yine de işlenmeye devam eder.
	Err error
}

// runBlockingLenses, bloklayıcı mercekleri (üçüncü-taraf/veri-erişimi/
// pazar-işlerliği — kind=="trending" tohumlarda +ürünleştirilebilirlik)
// userPrompt üzerinde SIRAYLA çalıştırır (#123, #153 — organik ve tohum
// yolunun TEK ortak uygulaması).
//
// stopOnFirstFail=true: organik yoldaki eski blockedByIdeaLens davranışı
// birebir — ilk "fail"de durur, kalan mercekler HİÇ ÇAĞRILMAZ (token
// tasarrufu); outcome tek merceğin adı+sebebini taşır.
//
// stopOnFirstFail=false: seeds.go'nun eski döngüsü birebir — TÜMÜ çalışır
// (veri-erişimi kararının her durumda karta yazılabilmesi ve "unsure"
// mercek(ler)in loglanabilmesi için); BİRDEN FAZLA mercek "fail" dönebilir,
// bu durumda outcome.Check/Reason ", "/"; " ile birleştirilmiş TÜM
// fail'lerin listesidir (mevcut log biçimiyle birebir) — fail baskındır.
//
// İKİ modda da "unsure" bloklamaz, yalnız "fail" bloklar. Mercek çağrısı
// HATA verirse (ağ/kota): ilk hatada durulur (kalan mercekler çağrılmaz),
// outcome.Err dolu döner, outcome.Check hata veren merceğin adıdır
// (loglama için) — bloklama YOK, iki yolda da aynı.
//
// verdicts, çağrılan mercek(ler)in ham kararlarıdır (hata durumunda hataya
// kadar olanlar) — veri-erişimi merceğinin ham kararını karta yazmak ve
// (tohum yolunda) "unsure" mercekleri loglamak için çağırana döner.
//
// llm.WithStage(ctx, "mercek") BURADA uygulanır.
func runBlockingLenses(ctx context.Context, chat llm.Chat, lenses []seedLens, userPrompt string, stopOnFirstFail bool) (gateOutcome, []lensVerdict) {
	ctx = llm.WithStage(ctx, "mercek")
	verdicts := make([]lensVerdict, len(lenses))
	for i, lens := range lenses {
		// Yargı çağrısı (bloklayıcı mercek): sıcaklık 0 — tutarlı karar (#106).
		raw, err := chat.ChatJSONWithTemperature(ctx, lens.system, userPrompt, 0)
		if err != nil {
			return gateOutcome{Check: lens.name, Err: err}, verdicts[:i]
		}
		verdicts[i] = parseLensVerdict(raw)
		if stopOnFirstFail && verdicts[i].Verdict == "fail" {
			return gateOutcome{Blocked: true, Stage: "blocking_lens", Check: lens.name, Reason: verdicts[i].Reason}, verdicts[:i+1]
		}
	}

	if !stopOnFirstFail {
		var failedNames, failedReasons []string
		for i, v := range verdicts {
			if v.Verdict == "fail" {
				failedNames = append(failedNames, lenses[i].name)
				failedReasons = append(failedReasons, v.Reason)
			}
		}
		if len(failedNames) > 0 {
			return gateOutcome{
				Blocked: true, Stage: "blocking_lens",
				Check: strings.Join(failedNames, ", "), Reason: strings.Join(failedReasons, "; "),
			}, verdicts
		}
	}

	return gateOutcome{}, verdicts
}

// evaluateDistinctiveness, distinctivenessCheck'i çağırıp (idea alanlarını
// doldurmaya DEVAM eder — mevcut davranış) sonucu tek bir gateOutcome'a
// yorumlar (#153): Blocked yalnız K1'de (doygunluk) true — kart yazılmaz.
// K2-K4 "fail" Blocked=false ama Stage="distinctiveness" + Criterion dolu
// döner (yalnız KAYIT için — applyGateOutcome hold() ÇAĞIRMAZ). "pass"/
// "unsure" Stage="" döner (kayıt yok). Mercek çağrısı HATA verirse
// outcome.Err dolu, Stage="" (kayıt yok, bloklama yok —
// distinctivenessCheck'in "alanlar NULL kalır" davranışı aynen korunur).
// llm.WithStage(ctx, "özgünlük") BURADA uygulanır.
func evaluateDistinctiveness(ctx context.Context, chat llm.Chat, idea *store.Idea) gateOutcome {
	ctx = llm.WithStage(ctx, "özgünlük")
	if err := distinctivenessCheck(ctx, chat, idea); err != nil {
		return gateOutcome{Err: err}
	}
	if idea.DistinctivenessVerdict == nil || *idea.DistinctivenessVerdict != "fail" {
		return gateOutcome{}
	}
	criterion := "none"
	if idea.DistinctivenessCriterion != nil {
		criterion = *idea.DistinctivenessCriterion
	}
	reason := ""
	if idea.DistinctivenessReason != nil {
		reason = *idea.DistinctivenessReason
	}
	return gateOutcome{
		Blocked:   criterion == "K1",
		Stage:     "distinctiveness",
		Criterion: criterion,
		Reason:    reason,
	}
}

// applyGateOutcome, gateOutcome'un yan etkilerini TEK yerde uygular (#153):
// eleme kaydı (recordElimination, best-effort) + bloklandıysa hold().
//
// o.Stage=="" (pass/unsure/mercek-özgünlük hatası) ise HİÇBİR ŞEY yapılmaz.
// o.Stage doluysa (yalnız "fail" verdict'lerinde — blocking_lens HER ZAMAN
// bloklar, distinctiveness yalnız K1'de) recordElimination HER ZAMAN
// çağrılır (K2-K4 dahil, best-effort — kendi hatasını yutar, pipeline'ı asla
// durdurmaz). hold yalnız o.Blocked ise çağrılır; organikte
// hold=MarkThemeIncoherent, tohumda hold=markProcessed. hold'un DÖNEN HATASI
// bu fonksiyondan ÇAĞIRANA döner — organik çağıran onu loglayıp devam eder
// (#151: damgalama hatası koşuyu durdurmaz), tohum çağıran ProcessSeeds'i
// durdurur (mevcut davranış, DEĞİŞTİRİLMEDİ — ayrıntı için seeds.go'daki
// markProcessed kullanım yerlerine bakın).
func applyGateOutcome(ctx context.Context, st *store.Store, o gateOutcome, subject, detail string, hold func() error) error {
	if o.Stage == "" {
		return nil
	}
	recordElimination(ctx, st, o.Stage, subject, "fail", o.Criterion, o.Reason, detail)
	if !o.Blocked {
		return nil
	}
	return hold()
}
