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

// gate.go (#153): kart olup olmama kararını veren 3 kontrolün (2 bloklayıcı
// mercek: üçüncü-taraf/veri-erişimi — #169: pazar-işlerliği eşsiz katkısı 0
// ölçüldüğünden kaldırıldı + özgünlük merceği) SONUCUNU tek tipte taşıyan ve
// sonucuna göre yapılan yan etkileri (eleme
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

// runBlockingLenses, bloklayıcı mercekleri (üçüncü-taraf/veri-erişimi —
// kind=="trending" tohumlarda +ürünleştirilebilirlik) userPrompt üzerinde
// SIRAYLA çalıştırır (#123, #153 — organik ve tohum
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
// N-oy (#197, PO kararı: özgünlükteki #181 oybirliği düzeni üçüncü-taraf
// merceğine de): lens.votes>1 olan mercek (yalnız üçüncü-taraf, config'ten —
// bkz. newLensSet) N kez oylanır (voteLens çekirdeği, blok oyu = "fail").
// Mercek ANCAK TÜM oylar "fail" derse "fail" sayılır (yukarıdaki iki mod
// bunu tek bir "fail" kararı gibi işler); oylar ayrışırsa mercek kararı
// "unsure" + "tartışmalı: ..." gerekçesidir ve BLOKLAMAZ; ilk oy hata verirse
// yukarıdaki hata yolu AYNEN işler, sonraki oy hatası tartışmalı sayılır.
// lens.votes<=1 ise (varsayılan, veri-erişimi/ürünleştirilebilirlik her
// zaman) eski tek-çağrı yolu DEĞİŞMEDEN çalışır.
//
// verdicts, çağrılan mercek(ler)in ham kararlarıdır (hata durumunda hataya
// kadar olanlar) — veri-erişimi merceğinin ham kararını karta yazmak ve
// (tohum yolunda) "unsure" mercekleri loglamak için çağırana döner. N-oylu
// merceğin elemanı NİHAİ karardır (ham oylar DEĞİL — onlar outcome.Verdicts'te).
//
// subject (#164): bu çağrının hangi girdi üzerinde çalıştığını taşır —
// "card" (üretilmiş kart alanları, organik yol) ya da "seed" (ham tohum
// alanları, tohum yolunun 2(-3) bloklayıcı merceği) — dönen gateOutcome.
// Verdicts'in her elemanına AYNEN yazılır (store.LensVerdict.Subject).
//
// llm.WithStage(ctx, "mercek") BURADA uygulanır.
func runBlockingLenses(ctx context.Context, chat llm.Chat, lenses []seedLens, userPrompt string, stopOnFirstFail bool, subject string) (gateOutcome, []lensVerdict) {
	ctx = llm.WithStage(ctx, "mercek")
	verdicts := make([]lensVerdict, len(lenses))
	var recorded []store.LensVerdict
	for i, lens := range lenses {
		if lens.votes > 1 {
			// N-oy yolu (#197): her oy AYRI bir yargı çağrısı (sıcaklık 0, #106).
			call := func(voteCtx context.Context) (lensVerdict, string, error) {
				raw, err := chat.ChatJSONWithTemperature(voteCtx, lens.system, userPrompt, 0)
				if err != nil {
					return lensVerdict{}, modelNameOf(chat), err
				}
				return parseLensVerdict(raw), modelNameOf(chat), nil
			}
			decision, voteRecords := voteLens(ctx, lens.votes, call, isThirdPartyBlock)
			recorded = append(recorded, lensVoteVerdicts(lens, subject, lens.votes, voteRecords)...)
			if decision.Err != nil {
				// İlk oyun KENDİSİ hata verdi: bugünkü tek-çağrı hata yolu birebir.
				return gateOutcome{Check: lens.name, Err: decision.Err, Verdicts: recorded}, verdicts[:i]
			}
			verdicts[i] = lensVerdict{Verdict: decision.Verdict, Criterion: decision.Criterion, Reason: decision.Reason}
			if decision.Disputed {
				log.Printf("mercek %q tartışmalı (konu: %s) — bloklamıyor: %s", lens.name, subject, decision.Reason)
			}
		} else {
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
		}
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

// lensVoteRecord, voteLens'in ÜRETTİĞİ TEK oyun kaydıdır (#181, #197) —
// evaluateDistinctiveness/runBlockingLenses bunu lensVoteVerdicts ile
// store.LensVerdict'e, lens-ab (lensab.go) kendi CSV satırının reason/tokens
// alanlarına eşler. Err doluysa Verdict sıfır değerdir (call hata döndürdü,
// LLM cevabı YOK).
type lensVoteRecord struct {
	Verdict lensVerdict
	Model   string
	Err     error
}

// lensVoteDecision, voteLens'in N oy üzerinden verdiği NİHAİ karardır —
// evaluateDistinctiveness bunu idea.Distinctiveness*/gateOutcome alanlarına,
// runBlockingLenses mercek kararına, lens-ab kendi satırının verdict/
// criterion'ına AYNEN yazar.
type lensVoteDecision struct {
	// Blocked: true ise N/N oy blok — kart bloklanır.
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

// lensVoteCall, voteLens'e geçirilen TEK oy çağrısıdır — call(ctx) bir LLM
// turudur. Üretimde (evaluateDistinctiveness) override+tek-seferlik-yedek
// mantığını İÇİNDE barındırır; lens-ab'de (lensab.go) oran-sınırı
// bekle-yeniden-dene döngüsünü İÇİNDE barındırır — voteLens bu ayrıntıları
// BİLMEZ, yalnız (verdict, model, hata) üçlüsünü okur.
type lensVoteCall func(ctx context.Context) (lensVerdict, string, error)

// isDistinctivenessBlock, özgünlük merceğinin "blok oyu" yüklemidir (#166,
// #181): verdict=="fail" && criterion ∈ {K1,K2} (doygunluk/yerleşik çözüm;
// K3/K4 fail blok SAYILMAZ).
func isDistinctivenessBlock(v lensVerdict) bool {
	return v.Verdict == "fail" && (v.Criterion == "K1" || v.Criterion == "K2")
}

// isThirdPartyBlock, üçüncü-taraf merceğinin "blok oyu" yüklemidir (#197):
// verdict=="fail" (bu merceğin criterion alanı yok — bloklayıcı mercek
// grubundaki genel kural: yalnız "fail" bloklar, "unsure" bloklamaz).
func isThirdPartyBlock(v lensVerdict) bool {
	return v.Verdict == "fail"
}

// voteLens, bir merceğin N-OY ÇEKİRDEĞİDİR (#181, PO kararı 2026-09-23:
// "oybirliğiyle blok" — ölçüm: kart 81'e 8 çağrıda 3 kez "fail K1" dedi, tek
// çağrıyla bloklamak yazı-tura; #197: aynı düzen üçüncü-taraf merceğine de).
// Kart ANCAK TÜM oylar blok derse bloklanır; oylar ayrışırsa kart YAZILIR ve
// "tartışmalı" işaretlenir, PO karar verir (KKK-20260921: mercek yalnız
// bariz olanı eler). SAF ve PAYLAŞILABİLİR: üretim (evaluateDistinctiveness,
// runBlockingLenses) ve `lens-ab --votes` AYNI bu fonksiyonu kullanır — iki
// kopya karar mantığı YOK; mercekler yalnız "blok oyu" yüklemiyle (isBlock)
// ayrışır (isDistinctivenessBlock / isThirdPartyBlock).
//
// Erken çıkışlı: k=1..n sırayla call(ctx) çağrılır.
//   - call HATA verirse: k==1 → dur, Decision.Err dolu (bugünkü tek-çağrı
//     davranışı birebir, diğer alanlar sıfır değer). k>1 → dur, oybirliği
//     kurulamadı: TARTIŞMALI (Err YOK, bloklama YOK) — hata oyu da votes
//     dönüşüne kaydedilir.
//   - Oy BLOK DEĞİLSE (pass/unsure/blok olmayan fail): dur. k==1 → sonuç
//     bugünküyle birebir (bu oyun kendi verdict/criterion/reason'ı,
//     Blocked=false). k>1 → TARTIŞMALI: Verdict "unsure", Criterion İLK blok
//     oyunun kriteri, Reason "tartışmalı: <blok>/<k> oy blok — " + İLK blok
//     oyunun gerekçesi (eliminationReasonLimit'e clip'lenir).
//   - Oy BLOK ise ve k<n: devam (bir sonraki oya geç). k==n (TÜMÜ blok):
//     BLOKLA — Verdict/Criterion/Reason İLK oyunkiler (tek oyda k==1==n
//     olduğundan zaten İLK oy — bugünkü blok yoluyla BİREBİR).
//
// n<=0 ise 1 sayılır (savunmacı — çağıranlar zaten >=1 garanti eder,
// config.Config.DistinctivenessVotes/ThirdPartyVotes de dahil).
func voteLens(ctx context.Context, n int, call lensVoteCall, isBlock func(lensVerdict) bool) (lensVoteDecision, []lensVoteRecord) {
	if n <= 0 {
		n = 1
	}

	votes := make([]lensVoteRecord, 0, n)
	blockCount := 0
	var firstBlock *lensVerdict

	for k := 1; k <= n; k++ {
		v, model, err := call(ctx)
		votes = append(votes, lensVoteRecord{Verdict: v, Model: model, Err: err})

		if err != nil {
			if k == 1 {
				return lensVoteDecision{Err: err}, votes
			}
			return disputedLensDecision(blockCount, k, firstBlock), votes
		}

		if !isBlock(v) {
			if k == 1 {
				return lensVoteDecision{
					Blocked: false, Verdict: v.Verdict, Criterion: v.Criterion, Reason: v.Reason,
				}, votes
			}
			return disputedLensDecision(blockCount, k, firstBlock), votes
		}

		blockCount++
		if firstBlock == nil {
			fb := v
			firstBlock = &fb
		}
		if k == n {
			return lensVoteDecision{
				Blocked: true, Verdict: "fail", Criterion: firstBlock.Criterion, Reason: firstBlock.Reason,
			}, votes
		}
	}
	// n>=1 olduğundan döngü yukarıda HER ZAMAN return eder — buraya erişilmez.
	panic("voteLens: erişilemez durum")
}

// voteDistinctiveness, voteLens'in özgünlük merceği için (isDistinctivenessBlock
// yüklemiyle) ince sarmalayıcısıdır (#181) — lens-ab ve birim testleri bu adı
// kullanır; karar mantığı voteLens'tedir, burada KOPYA YOK.
func voteDistinctiveness(ctx context.Context, n int, call lensVoteCall) (lensVoteDecision, []lensVoteRecord) {
	return voteLens(ctx, n, call, isDistinctivenessBlock)
}

// disputedLensDecision, oyların AYRIŞTIĞI (en az bir blok oy, ardından blok
// olmayan bir oy YA DA bir oy hatası) TARTIŞMALI kararı kurar. firstBlock
// (dispute yalnız EN AZ bir blok oydan SONRA tetiklendiği için) HER ZAMAN dolu
// gelir — nil kontrolü yalnız savunmacılık.
func disputedLensDecision(blockCount, k int, firstBlock *lensVerdict) lensVoteDecision {
	criterion, reason := "", fmt.Sprintf("tartışmalı: %d/%d oy blok", blockCount, k)
	if firstBlock != nil {
		criterion = firstBlock.Criterion
		reason = clip(fmt.Sprintf("tartışmalı: %d/%d oy blok — %s", blockCount, k, firstBlock.Reason), eliminationReasonLimit)
	}
	return lensVoteDecision{Disputed: true, Verdict: "unsure", Criterion: criterion, Reason: reason}
}

// lensVoteVerdicts, voteLens'in oy kayıtlarını kalıcı store.LensVerdict
// kayıtlarına çevirir (#164, #181, #197) — evaluateDistinctiveness ve
// runBlockingLenses'in TEK ortak biçimi: her oy AYRI bir kayıt, hata oyu
// Verdict="error" + hata metni (500 rune'a clip), n>1 iken (YAPILAN oy
// sayısından bağımsız, yapılandırılan n) Reason'ın başına "oy k/N: " öneki.
func lensVoteVerdicts(lens seedLens, subject string, n int, votes []lensVoteRecord) []store.LensVerdict {
	recorded := make([]store.LensVerdict, 0, len(votes))
	for i, vr := range votes {
		verdict, reason := vr.Verdict.Verdict, vr.Verdict.Reason
		if vr.Err != nil {
			verdict = "error"
			reason = clip(vr.Err.Error(), eliminationReasonLimit)
		}
		if n > 1 {
			reason = fmt.Sprintf("oy %d/%d: %s", i+1, n, reason)
		}
		recorded = append(recorded, store.LensVerdict{
			Lens: lens.name, PromptVersion: lens.version, Subject: subject,
			Verdict: verdict, Reason: reason, Model: vr.Model, At: time.Now().UTC(),
		})
	}
	return recorded
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

	recorded := lensVoteVerdicts(
		seedLens{name: lensName, version: lensDistinctivenessVersion}, "card", n, voteRecords)

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
