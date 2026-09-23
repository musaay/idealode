package pipeline

import (
	"context"
	"fmt"
	"log"
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
	// Criterion: yalnız Stage=="distinctiveness" iken K1..K4 dolu (K1|K2
	// bloklar — bkz. evaluateDistinctiveness).
	Criterion string
	// Reason: bloklayan/fail sebebi (tohum yolunda birden fazla mercek
	// "fail" dönerse "; " ile birleştirilmiş sebepler).
	Reason string
	// Err: mercek/özgünlük ÇAĞRI hatası (ağ/kota) — bloklamaz, çağıran
	// loglar, kart/tohum yine de işlenmeye devam eder.
	Err error
	// Disputed (#181, PO kararı 2026-09-23): yalnız özgünlük N-oy yolunda
	// (evaluateDistinctiveness, N>1) true olabilir — oylar AYRIŞTI (en az
	// bir blok oy, sonra blok OLMAYAN bir oy ya da bir oy hatası): kart
	// YAZILIR (Blocked=false, Stage="" — eliminations'a KAYIT YOK), yalnız
	// gözlemlenebilirlik/log için işaretlenir. applyGateOutcome bu alana
	// BAKMAZ.
	Disputed bool
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
				Verdict: "error", Reason: clip(err.Error(), eliminationReasonLimit), Model: modelNameOf(chat), At: time.Now().UTC(),
			})
			return gateOutcome{Check: lens.name, Err: err, Verdicts: recorded}, verdicts[:i]
		}
		verdicts[i] = parseLensVerdict(raw)
		recorded = append(recorded, store.LensVerdict{
			Lens: lens.name, PromptVersion: lens.version, Subject: subject,
			Verdict: verdicts[i].Verdict, Reason: verdicts[i].Reason, Model: modelNameOf(chat), At: time.Now().UTC(),
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

// distinctivenessVoteRecord, voteDistinctiveness'in ÜRETTİĞİ TEK oyun
// kaydıdır (#181) — evaluateDistinctiveness bunu store.LensVerdict'e,
// lens-ab (lensab.go) kendi CSV satırının reason/tokens alanlarına eşler.
// Err doluysa Verdict sıfır değerdir (call hata döndürdü, LLM cevabı YOK).
type distinctivenessVoteRecord struct {
	Verdict lensVerdict
	Model   string
	Err     error
}

// distinctivenessDecision, voteDistinctiveness'in N oy üzerinden verdiği
// NİHAİ karardır — evaluateDistinctiveness bunu idea.Distinctiveness*/
// gateOutcome alanlarına, lens-ab kendi satırının verdict/criterion'ına
// AYNEN yazar.
type distinctivenessDecision struct {
	// Blocked: true ise N/N oy blok (fail K1|K2) — kart bloklanır.
	Blocked bool
	// Disputed: true ise oylar AYRIŞTI (en az bir blok oy, sonra blok
	// OLMAYAN bir oy YA DA bir oy hatası) — kart YAZILIR, "tartışmalı"
	// işaretlenir (PO karar verir, KKK-20260921).
	Disputed bool
	// Verdict/Criterion/Reason: nihai karar — Disputed'ta Verdict="unsure".
	Verdict   string
	Criterion string
	Reason    string
	// Err: yalnız k==1'İN KENDİSİ hata verdiğinde dolu (bugünkü tek-çağrı
	// davranışı birebir) — bu durumda diğer alanlar sıfır değerdir.
	Err error
}

// voteDistinctivenessCall, voteDistinctiveness'e geçirilen TEK oy
// çağrısıdır — call(ctx) bir LLM turudur. Üretimde (evaluateDistinctiveness)
// override+tek-seferlik-yedek mantığını İÇİNDE barındırır; lens-ab'de
// (lensab.go) oran-sınırı bekle-yeniden-dene döngüsünü İÇİNDE barındırır —
// voteDistinctiveness bu ayrıntıları BİLMEZ, yalnız (verdict, model, hata)
// üçlüsünü okur.
type voteDistinctivenessCall func(ctx context.Context) (lensVerdict, string, error)

// voteDistinctiveness, özgünlük merceğinin N-OY ÇEKİRDEĞİDİR (#181, PO
// kararı 2026-09-23: "oybirliğiyle blok" — ölçüm: kart 81'e 8 çağrıda 3 kez
// "fail K1" dedi, tek çağrıyla bloklamak yazı-tura). Kart ANCAK TÜM oylar
// blok derse bloklanır; oylar ayrışırsa kart YAZILIR ve "tartışmalı"
// işaretlenir, PO karar verir (KKK-20260921: mercek yalnız bariz olanı
// eler). SAF ve PAYLAŞILABİLİR: hem üretim (evaluateDistinctiveness) hem
// `lens-ab --votes` AYNI bu fonksiyonu kullanır — iki kopya karar mantığı
// YOK.
//
// "Blok oyu" = verdict=="fail" && criterion ∈ {K1,K2} (doygunluk/yerleşik
// çözüm — #166; K3/K4 fail blok SAYILMAZ). Erken çıkışlı: k=1..n sırayla
// call(ctx) çağrılır.
//   - call HATA verirse: k==1 → dur, Decision.Err dolu (bugünkü tek-çağrı
//     davranışı birebir, diğer alanlar sıfır değer). k>1 → dur, oybirliği
//     kurulamadı: TARTIŞMALI (Err YOK, bloklama YOK) — hata oyu da votes
//     dönüşüne kaydedilir.
//   - Oy BLOK DEĞİLSE (pass/unsure/K3-K4 fail): dur. k==1 → sonuç
//     bugünküyle birebir (bu oyun kendi verdict/criterion/reason'ı,
//     Blocked=false — K1|K2 dışında hiçbir tek oy tek başına bloklamaz).
//     k>1 → TARTIŞMALI: Verdict "unsure", Criterion İLK blok oyunun
//     kriteri, Reason "tartışmalı: <blok>/<k> oy blok — " + İLK blok
//     oyunun gerekçesi (eliminationReasonLimit'e clip'lenir).
//   - Oy BLOK ise ve k<n: devam (bir sonraki oya geç). k==n (TÜMÜ blok):
//     BLOKLA — Verdict/Criterion/Reason İLK oyunkiler (tek oyda k==1==n
//     olduğundan zaten İLK oy — bugünkü blok yoluyla BİREBİR).
//
// n<=0 ise 1 sayılır (savunmacı — çağıranlar zaten >=1 garanti eder,
// config.Config.DistinctivenessVotes de dahil).
func voteDistinctiveness(ctx context.Context, n int, call voteDistinctivenessCall) (distinctivenessDecision, []distinctivenessVoteRecord) {
	if n <= 0 {
		n = 1
	}

	votes := make([]distinctivenessVoteRecord, 0, n)
	blockCount := 0
	var firstBlock *lensVerdict

	for k := 1; k <= n; k++ {
		v, model, err := call(ctx)
		votes = append(votes, distinctivenessVoteRecord{Verdict: v, Model: model, Err: err})

		if err != nil {
			if k == 1 {
				return distinctivenessDecision{Err: err}, votes
			}
			return disputedDistinctivenessDecision(blockCount, k, firstBlock), votes
		}

		isBlock := v.Verdict == "fail" && (v.Criterion == "K1" || v.Criterion == "K2")
		if !isBlock {
			if k == 1 {
				return distinctivenessDecision{
					Blocked: false, Verdict: v.Verdict, Criterion: v.Criterion, Reason: v.Reason,
				}, votes
			}
			return disputedDistinctivenessDecision(blockCount, k, firstBlock), votes
		}

		blockCount++
		if firstBlock == nil {
			fb := v
			firstBlock = &fb
		}
		if k == n {
			return distinctivenessDecision{
				Blocked: true, Verdict: "fail", Criterion: firstBlock.Criterion, Reason: firstBlock.Reason,
			}, votes
		}
	}
	// n>=1 olduğundan döngü yukarıda HER ZAMAN return eder — buraya erişilmez.
	panic("voteDistinctiveness: erişilemez durum")
}

// disputedDistinctivenessDecision, oyların AYRIŞTIĞI (en az bir blok oy,
// ardından blok olmayan bir oy YA DA bir oy hatası) TARTIŞMALI kararı
// kurar. firstBlock (dispute yalnız EN AZ bir blok oydan SONRA tetiklendiği
// için) HER ZAMAN dolu gelir — nil kontrolü yalnız savunmacılık.
func disputedDistinctivenessDecision(blockCount, k int, firstBlock *lensVerdict) distinctivenessDecision {
	criterion, reason := "", fmt.Sprintf("tartışmalı: %d/%d oy blok", blockCount, k)
	if firstBlock != nil {
		criterion = firstBlock.Criterion
		reason = clip(fmt.Sprintf("tartışmalı: %d/%d oy blok — %s", blockCount, k, firstBlock.Reason), eliminationReasonLimit)
	}
	return distinctivenessDecision{Disputed: true, Verdict: "unsure", Criterion: criterion, Reason: reason}
}

// evaluateDistinctiveness, özgünlük merceğini votes kez oylatıp (#181,
// voteDistinctiveness çekirdeğiyle) kararı tek bir gateOutcome'a yorumlar
// (#153, #166): Blocked yalnız TÜM oylar K1 (doygunluk) VE/veya K2 (yerleşik
// çözüm) fail derse true — kart yazılmaz. Oylar ayrışırsa (Disputed) kart
// YAZILIR, "unsure" işaretlenir, Stage="" (eliminations'a KAYIT YOK — kart
// elenmedi, yalnız gözlemlenebilir işaretlendi). K3-K4 (N=1 yolunda) "fail"
// Blocked=false ama Stage="distinctiveness" + Criterion dolu döner (yalnız
// KAYIT için — applyGateOutcome hold() ÇAĞIRMAZ). "pass"/"unsure" (tek oy
// ya da N=1) Stage="" döner (kayıt yok). k==1'İN KENDİSİ hata verirse
// outcome.Err dolu, Stage="" (kayıt yok, bloklama yok — bugünkü davranış
// birebir); k>1 hatası TARTIŞMALI sayılır (Err YOK). llm.WithStage(ctx,
// "özgünlük") BURADA uygulanır.
//
// votes: config.Config.DistinctivenessVotes'tan gelir (<=0 ise 1 sayılır) —
// VARSAYILAN 1 ile bugünkü tek-çağrı davranışı BİREBİR korunur.
//
// #164, #181: dönen gateOutcome.Verdicts votes kadar elemanlıdır (her oy
// AYRI bir store.LensVerdict, Subject="card"); votes>1 iken her kaydın
// Reason'ının başına "oy k/N: " öneki eklenir. Çağıran (synthesize.go/
// seeds.go) bunu kendi bloklayıcı mercek Verdicts'iyle BİRLEŞTİRİR (kart hiç
// yazılmadan elenen durumda tek kalıcı yer eliminations.verdicts olur).
//
// distinctChat (#166, opsiyonel — trailing variadic, geriye dönük uyumlu
// çağrı imzası): doluysa (cmd/idealode, DISTINCTIVENESS_LLM_* üçü de
// tanımlıysa) HER OYUN çağrısı ÖNCE bu istemciyle denenir; o istemci hata
// verirse (ağ/kota/413/json_validate) AYNI çağrı BİR KEZ chat (varsayılan
// istemci) ile tekrar denenir — ikisi de hata verirse o OYUN kendisi hata
// sayılır (voteDistinctiveness'in k==1/k>1 kuralına göre işlenir). ctx
// zaten iptal edildiyse (ctx.Err()!=nil) yedek deneme ATLANIR. distinctChat
// boşsa (çağrılmadıysa ya da nil) doğrudan chat kullanılır — bugünkü
// davranışla birebir. Kalıcı kayıttaki Model alanı O OYUN kararını hangi
// istemcinin verdiğini taşır (yedeğe düşüldüyse chat'in modeli).
func evaluateDistinctiveness(ctx context.Context, chat llm.Chat, idea *store.Idea, votes int, distinctChat ...llm.Chat) gateOutcome {
	ctx = llm.WithStage(ctx, "özgünlük")
	const lensName = "özgünlük"

	n := votes
	if n <= 0 {
		n = 1
	}

	var override llm.Chat
	if len(distinctChat) > 0 {
		override = distinctChat[0]
	}

	// call: TEK bir oyun çağrısı — override+tek-seferlik-yedek mantığı
	// (#166) HER OYDA BAĞIMSIZ uygulanır (spec #181: "yedek istemci kuralı
	// her oy için aynı").
	call := func(voteCtx context.Context) (lensVerdict, string, error) {
		active := chat
		usingOverride := override != nil
		if usingOverride {
			active = override
		}
		v, err := distinctivenessRaw(voteCtx, active, idea)
		// ctx.Err() kontrolü: koşu iptal edildiyse (ör. bağlam deadline/
		// cancel) yedek denemeyi ATLA — ikinci çağrı da anında aynı iptal
		// hatasını dönecektir, boşuna bir HTTP denemesi/log kirliliği
		// yaratmayalım.
		if err != nil && usingOverride && voteCtx.Err() == nil {
			log.Printf("özgünlük: ayrı istemci (%s) hata verdi, varsayılan istemciye tek seferlik yedek deneme: %v", modelNameOf(active), err)
			active = chat
			v, err = distinctivenessRaw(voteCtx, active, idea)
		}
		return v, modelNameOf(active), err
	}

	decision, voteRecords := voteDistinctiveness(ctx, n, call)

	recorded := make([]store.LensVerdict, 0, len(voteRecords))
	for i, vr := range voteRecords {
		verdict, reason := vr.Verdict.Verdict, vr.Verdict.Reason
		if vr.Err != nil {
			verdict = "error"
			reason = clip(vr.Err.Error(), eliminationReasonLimit)
		}
		if n > 1 {
			reason = fmt.Sprintf("oy %d/%d: %s", i+1, n, reason)
		}
		recorded = append(recorded, store.LensVerdict{
			Lens: lensName, PromptVersion: lensDistinctivenessVersion, Subject: "card",
			Verdict: verdict, Reason: reason, Model: vr.Model, At: time.Now().UTC(),
		})
	}

	if decision.Err != nil {
		return gateOutcome{Err: decision.Err, Verdicts: recorded}
	}

	// Nihai kararın alanları karta AYNEN yazılır (bugünkü distinctivenessCheck
	// mutasyonunun N-oy karşılığı) — Disputed'ta "unsure"+ilk blok oyunun K'si.
	idea.DistinctivenessVerdict = &decision.Verdict
	idea.DistinctivenessCriterion = &decision.Criterion
	idea.DistinctivenessReason = &decision.Reason

	if decision.Verdict != "fail" {
		return gateOutcome{Verdicts: recorded, Disputed: decision.Disputed}
	}
	return gateOutcome{
		Blocked:   decision.Blocked,
		Stage:     "distinctiveness",
		Check:     lensName,
		Criterion: decision.Criterion,
		Verdicts:  recorded,
		Reason:    decision.Reason,
	}
}

// applyGateOutcome, gateOutcome'un yan etkilerini TEK yerde uygular (#153):
// eleme kaydı (recordElimination, best-effort) + bloklandıysa hold().
//
// o.Stage=="" (pass/unsure/mercek-özgünlük hatası) ise HİÇBİR ŞEY yapılmaz.
// o.Stage doluysa (yalnız "fail" verdict'lerinde — blocking_lens HER ZAMAN
// bloklar, distinctiveness K1|K2'de, #166) recordElimination HER ZAMAN
// çağrılır (K3-K4 dahil, best-effort — kendi hatasını yutar, pipeline'ı asla
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

// modelNameOf, verilen chat istemcisinin model adını döner (#166) —
// istemci llm.NamedChat uyguluyorsa (canlıda *llm.OpenAICompatClient hep
// uygular) o adı, uygulamıyorsa (sahte test istemcileri, isterse kendisi de
// uygulayabilir) boş string döner. Yalnız gözlemlenebilirlik
// (store.LensVerdict.Model) için — davranışı ETKİLEMEZ.
func modelNameOf(chat llm.Chat) string {
	if nc, ok := chat.(llm.NamedChat); ok {
		return nc.ModelName()
	}
	return ""
}
