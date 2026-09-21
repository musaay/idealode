package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
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
	// Verdicts (#164): bu kapı kontrolünde yapılan TÜM mercek çağrılarının
	// (pass/fail/unsure/error) kalıcı kaydı — ideas.lens_verdicts /
	// eliminations.verdicts jsonb kolonlarına AYNEN yazılır. Hata
	// durumunda hataya kadar yapılan çağrılar + hatanın kendisi
	// (Verdict="error") dahildir. nil olabilir (henüz hiç çağrı
	// yapılmadıysa) — yazım sınırında (InsertIdea/InsertElimination) boş
	// diziye indirgenir, çağıran burada guard ETMEK ZORUNDA DEĞİL.
	Verdicts []store.LensVerdict
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
// subject (#164): bu çağrının hangi girdi üzerinde çalıştığını taşır —
// "card" (üretilmiş kart alanları, organik yol) ya da "seed" (ham tohum
// alanları, tohum yolunun 3(-4) bloklayıcı merceği) — dönen gateOutcome.
// Verdicts'in her elemanına AYNEN yazılır (store.LensVerdict.Subject).
//
// llm.WithStage(ctx, "mercek") BURADA uygulanır.
func runBlockingLenses(ctx context.Context, chat llm.Chat, lenses []seedLens, userPrompt string, stopOnFirstFail bool, subject string) (gateOutcome, []lensVerdict) {
	ctx = llm.WithStage(ctx, "mercek")
	verdicts := make([]lensVerdict, len(lenses))
	var recorded []store.LensVerdict
	for i, lens := range lenses {
		// Yargı çağrısı (bloklayıcı mercek): sıcaklık 0 — tutarlı karar (#106).
		raw, err := chat.ChatJSONWithTemperature(ctx, lens.system, userPrompt, 0)
		if err != nil {
			// #164: çağrı hatası verdict="error" + hata metniyle (500 rune'a
			// kırpılmış) kalıcı kayda eklenir — NULL (hiç çağrılmadı) ile
			// karışmasın diye. Bloklama/mevcut davranış DEĞİŞMEZ.
			recorded = append(recorded, store.LensVerdict{
				Lens: lens.name, PromptVersion: lens.version, Subject: subject,
				Verdict: "error", Reason: clip(err.Error(), eliminationReasonLimit), At: time.Now().UTC(),
			})
			return gateOutcome{Check: lens.name, Err: err, Verdicts: recorded}, verdicts[:i]
		}
		verdicts[i] = parseLensVerdict(raw)
		recorded = append(recorded, store.LensVerdict{
			Lens: lens.name, PromptVersion: lens.version, Subject: subject,
			Verdict: verdicts[i].Verdict, Reason: verdicts[i].Reason, At: time.Now().UTC(),
		})
		if stopOnFirstFail && verdicts[i].Verdict == "fail" {
			return gateOutcome{Blocked: true, Stage: "blocking_lens", Check: lens.name, Reason: verdicts[i].Reason, Verdicts: recorded}, verdicts[:i+1]
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
				Verdicts: recorded,
			}, verdicts
		}
	}

	return gateOutcome{Verdicts: recorded}, verdicts
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
//
// #164: dönen gateOutcome.Verdicts TEK elemanlıdır (özgünlük merceğinin
// kendi çağrısı, Subject="card" — özgünlük her iki yolda da kart
// üretildikten SONRA kart alanları üzerinde çalışır). Çağıran (synthesize.go/
// seeds.go) bunu kendi bloklayıcı mercek Verdicts'iyle BİRLEŞTİRİR (kart hiç
// yazılmadan elenen durumda tek kalıcı yer eliminations.verdicts olur).
func evaluateDistinctiveness(ctx context.Context, chat llm.Chat, idea *store.Idea) gateOutcome {
	ctx = llm.WithStage(ctx, "özgünlük")
	const lensName = "özgünlük"
	if err := distinctivenessCheck(ctx, chat, idea); err != nil {
		return gateOutcome{Err: err, Verdicts: []store.LensVerdict{{
			Lens: lensName, PromptVersion: lensDistinctivenessVersion, Subject: "card",
			Verdict: "error", Reason: clip(err.Error(), eliminationReasonLimit), At: time.Now().UTC(),
		}}}
	}
	verdict := "unsure"
	if idea.DistinctivenessVerdict != nil {
		verdict = *idea.DistinctivenessVerdict
	}
	reason := ""
	if idea.DistinctivenessReason != nil {
		reason = *idea.DistinctivenessReason
	}
	recorded := []store.LensVerdict{{
		Lens: lensName, PromptVersion: lensDistinctivenessVersion, Subject: "card",
		Verdict: verdict, Reason: reason, At: time.Now().UTC(),
	}}
	if idea.DistinctivenessVerdict == nil || *idea.DistinctivenessVerdict != "fail" {
		return gateOutcome{Verdicts: recorded}
	}
	criterion := "none"
	if idea.DistinctivenessCriterion != nil {
		criterion = *idea.DistinctivenessCriterion
	}
	return gateOutcome{
		Blocked:   criterion == "K1",
		Stage:     "distinctiveness",
		Check:     lensName,
		Criterion: criterion,
		Verdicts:  recorded,
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
	// #164: o.Check (elemeyi yapan merceğin adı) + o.Verdicts (o ana kadar
	// yapılan TÜM mercek çağrıları) eliminations satırına aynen taşınır —
	// kart hiç yazılmadan elenen durumda bu, mercek kararlarının TEK kalıcı
	// yeridir (çağıran gerekirse o.Verdicts'i kendi biriktirdiği daha geniş
	// listeyle DEĞİŞTİREBİLİR — bkz. synthesize.go/seeds.go).
	recordElimination(ctx, st, o.Stage, subject, "fail", o.Criterion, o.Reason, detail, o.Check, o.Verdicts)
	if !o.Blocked {
		return nil
	}
	return hold()
}
