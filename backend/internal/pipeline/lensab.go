package pipeline

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

// lensab.go (#165, üst plan #163 §5/§6.2; genişletme #175): `idealode
// lens-ab` komutunun gövdesi — altın set üzerinde v1/v3 (ya da --prompt-file
// ile verilen bir aday) mercek prompt'larını canlı LLM'e karşı karşılaştırır.
// HİÇBİR ŞEY DB'ye/eliminations'a YAZMAZ (yalnız GetIdeaForAudit/
// GetElimination ile OKUR); mevcut mercekleri (synthesize.go/seeds.go/
// gate.go) hiçbir şekilde ÇAĞIRMAZ ya da ETKİLEMEZ — ayrı, salt-ölçüm bir
// çağrı yoludur. #175: canlı kullanımda görülen sorunlara karşı — istekler
// arası bekleme (--sleep-ms), oran sınırı sonrası bekle-yeniden-dene
// (--rate-wait-sec/--rate-retries), oran-sınırı-DIŞI hata/girdi hatasında
// koşuyu bitirmeden "error" satırı yazıp devam etme, anında/akan CSV yazımı
// (--out + --resume), CSV'de gerekçe (reason) + model sütunu, üretimle AYNI
// istemci seçimi (özgünlükte distinctChat, diğerlerinde chat).

// GoldenCase, testdata/lens-golden.json'daki tek satır (#165 §5'teki
// tablodan üretildi). Kind=="idea" ise DB'den ideaLensUserPrompt kurulur
// (GetIdeaForAudit); Kind=="elimination" ise eliminations satırının
// subject/detail alanlarından (GetElimination).
type GoldenCase struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"` // idea | elimination
	// Lens: third_party | data_access | market_viability | distinctiveness
	// (lensRegistry'nin kanonik anahtarları — v1/v3 sabitlerine BURADAN
	// eşlenir).
	Lens string `json:"lens"`
	// Expect: tek beklenen verdict (pass|fail|unsure). ExpectAny doluysa
	// Expect yok sayılır (#165: "veri-erişiminde 81 için pass|unsure kabul").
	Expect    string   `json:"expect,omitempty"`
	ExpectAny []string `json:"expect_any,omitempty"`
	// Criterion: yalnız distinctiveness'ta ve yalnız "fail" beklenirken
	// dolu — mercek doğru K'yi bulmalı (yanlış K etiket hatası sayılır,
	// #165 kabul eşiği).
	Criterion string `json:"criterion,omitempty"`
	// Watch: "İzleme" satırı (#165 §5) — uyum/tekrarlanabilirlik yüzdesine
	// GİRMEZ, özet raporda AYRI listelenir.
	Watch bool `json:"watch,omitempty"`
}

// lensDef, kanonik mercek kimliğinin v1 = canlı sürüm (üretim sabitleri
// lensThirdPartySystem/lensDataAccessSystem/lensMarketViabilitySystem/
// lensDistinctivenessSystem — "v1" etiketi tarihsel, içerik özgünlükte #166
// ile v4 metnine yükseldi ama lens-ab bunu hâlâ "v1" diye ölçer) ve v3 =
// aday (lens_prompts_v3.go'daki *V3 sabitleri) sistem prompt'unu taşır.
type lensDef struct {
	v1, v3 string
}

// lensRegistry, GoldenCase.Lens'in tanıdığı DÖRT kanonik kimlik — #163
// §4/§5'teki dosya adlarıyla aynı köke sahip, kısa/sabit anahtarlar.
var lensRegistry = map[string]lensDef{
	"third_party":      {lensThirdPartySystem, lensThirdPartySystemV3},
	"data_access":      {lensDataAccessSystem, lensDataAccessSystemV3},
	"market_viability": {lensMarketViabilitySystem, lensMarketViabilitySystemV3},
	"distinctiveness":  {lensDistinctivenessSystem, lensDistinctivenessSystemV3},
}

// LensABRowKey, --resume'da bir (çift, koşu) sonucunu tekil belirleyen
// anahtar (#175) — CSV'de zaten var olan (verdict!="error") bir satırın
// koşusu bu anahtarla ATLANIR (LLM tekrar çağrılmaz).
type LensABRowKey struct {
	ID            int64
	Kind          string
	Lens          string
	PromptVersion string
	Run           int
}

// LensABOptions, RunLensAB'nin çalışma parametreleri (idealode lens-ab
// bayraklarının doğrudan karşılığı).
type LensABOptions struct {
	// Lens: kanonik mercek kimliği (lensRegistry anahtarı) ya da "all"
	// (varsayılan — set'teki her satır kendi Lens'iyle koşar).
	Lens string
	// PromptVersion: "v1" ya da "v3" — PromptText boşsa ZORUNLU, set'teki
	// HER satır için AYNI sürüm kullanılır (A/B karşılaştırması iki ayrı
	// komut koşusuyla yapılır).
	PromptVersion string
	// PromptText: doluysa (--prompt-file'dan okunan) sistem prompt METNİ —
	// PromptVersion YOK SAYILIR, mercek seçimi hâlâ lensRegistry'den
	// (Lens'in var olduğunu doğrulamak için) yapılır ama sistem prompt'u
	// SABİT bu metindir. Yalnız Lens != "all" iken geçerli (RunLensAB
	// aksi halde hata döner) — bir aday prompt'u tüm dört mercekte AYNI
	// anlama gelmez. Dosya okuması BURADA yapılmaz (main.go'nun işi) —
	// testte doğrudan içerik verilebilir (#175).
	PromptText string
	// PromptLabel: PromptText doluyken CSV/özet'teki "prompt" kolonunun
	// değeri ("file:<taban ad>" — main.go doldurur). Boşsa "file" kullanılır.
	PromptLabel string
	// Runs: çift başına koşu sayısı — tekrarlanabilirlik için >=2 (#165
	// §5 kabul eşiği: "iki koşuda aynı verdict"). <=0 ise 1 sayılır.
	Runs int
	// BudgetTokens: birikimli token bu değere ULAŞTIĞINDA/AŞTIĞINDA durur
	// (kaldığı çift+koşu StoppedAtCase/StoppedAtRun'a yazılır). <=0 =
	// sınırsız.
	BudgetTokens int
	// SleepAfterCall: her LLM çağrısından (başarılı ya da başarısız, oran
	// sınırı yeniden denemeleri DAHİL) SONRA beklenecek süre — Gemini gibi
	// dakikalık istek sınırı düşük sağlayıcılarda 429'u BAŞTAN azaltır
	// (#175). <=0 = beklemez. ctx iptaline duyarlı (iptalde RunLensAB
	// hemen durur — bu bir "hata satırı" değil, GERÇEK bir iptaldir).
	SleepAfterCall time.Duration
	// RateWait: istemcinin KENDİ yeniden denemeleri (llm paketindeki
	// maxRetries) tükendikten SONRA hâlâ oran sınırı (llm.IsRateLimited)
	// dönen bir çağrı için AYNI çağrıyı tekrar denemeden önce beklenecek
	// süre.
	RateWait time.Duration
	// RateRetries: RateWait ile kaç kez daha denenir — tükenirse o (çift,
	// koşu) için "error" satırı yazılır, koşu DEVAM EDER (bitmez).
	RateRetries int
	// Votes (#181, "--votes N"): distinctiveness satırlarında her "run"
	// üretimdeki voteDistinctiveness çekirdeğiyle (gate.go) N oy çağrısı
	// yapar — İKİ KOPYA KARAR MANTIĞI YOK. CSV satırı NİHAİ kararı taşır
	// (verdict/criterion), tokens tüm oyların TOPLAMI, reason
	// "oylar: v1,v2,... - karar gerekçesi" biçimindedir. <=1 ise (varsayılan)
	// BUGÜNKÜ tek-çağrılık davranış BİREBİR korunur — hiçbir satır formatı
	// değişmez. >1 iken Lens != "distinctiveness" HATA döner (bkz. RunLensAB
	// başı) — bir oylama kararı yalnız özgünlük merceği için tanımlıdır.
	Votes int
	// ExistingRows: --resume'da VAR OLAN CSV'den okunmuş eski satırlar —
	// bu koşuda YENİDEN ÜRETİLMEZ (SkipKeys zaten engeller), yalnız
	// Summaries hesabına eski+yeni BİRLİKTE girsin diye taşınır (LensABResult.
	// Rows bu koşuda ÜRETİLEN satırları içerir, ExistingRows'u TEKRARLAMAZ).
	ExistingRows []LensABRow
	// SkipKeys: --resume'da zaten tamamlanmış (verdict!="error")
	// (id,kind,lens,prompt,run) anahtarları — bu koşularda LLM ÇAĞRILMAZ,
	// girdi (DB) bile çekilmez (bir çiftin TÜM koşuları zaten tamamsa o
	// çift baştan atlanır).
	SkipKeys map[LensABRowKey]bool
	// OnRow: doluysa RunLensAB her YENİ satırı ÜRETİLDİĞİ ANDA bu
	// callback'e verir (main.go bunu CSV'ye anında yazıp Flush etmek için
	// kullanır, #175 madde B — süreç ortada ölse de o ana kadarki satırlar
	// dosyada kalır). err dönerse RunLensAB durur (örn. disk dolu).
	OnRow func(LensABRow) error
}

// LensABRow, CSV'nin/raporun tek satırı — bir (çift, koşu) sonucudur.
type LensABRow struct {
	ID            int64
	Kind          string // idea | elimination
	Lens          string
	PromptVersion string // "v1" | "v3" | "file:<taban ad>" (CSV "prompt" kolonu)
	Run           int
	Verdict       string // pass | fail | unsure | error (llm hatası/oran sınırı tükendi/girdi hatası)
	Criterion     string
	Expect        string
	Match         bool
	Tokens        int
	Watch         bool // CSV'ye yazılmaz — özet raporunda ayrı sayılır
	// Model: llm.NamedChat.ModelName() — istemci uygulamıyorsa boş (#175).
	Model string
	// Reason: BAŞARILI çağrıda lensVerdict.Reason, hata satırında hata
	// metni — ikisi de 200 karaktere kırpılır (truncateReason).
	Reason string
}

// LensABLensSummary, mercek başına özet (rapor sonunda).
type LensABLensSummary struct {
	Lens string
	// Cases/Matches: İZLEME HARİÇ ve run=1'i HATA OLMAYAN çiftler
	// üzerinden, run=1'in Match'ine göre (#175 madde E: hata satırları
	// paydadan hariç).
	Cases    int
	Matches  int
	MatchPct float64
	// RepeatableTotal/RepeatableCases: yalnız HATA OLMAYAN koşuları Runs>=2
	// olan çiftler üzerinden — o koşularda AYNI verdict çıkanların oranı.
	RepeatableTotal int
	RepeatableCases int
	RepeatablePct   float64
	TotalTokens     int
	// BlockRepeatableTotal/BlockRepeatableCases (#181): RepeatableXxx'ten
	// FARKLI bir tekrarlanabilirlik — verdict/criterion BİREBİR AYNI olmak
	// yerine yalnız "blok mu değil mi" kararının kart başına TÜM koşularda
	// AYNI olup olmadığına bakar (blok = distinctiveness'ta fail&K1|K2,
	// diğer merceklerde herhangi bir fail). K1 fail vs K2 fail FARKLI
	// verdict/criterion'dır ama ikisi de "blok" — RepeatableCases'te
	// tekrarlanamaz sayılır, BlockRepeatableCases'te sayılır. Aynı payda
	// (Runs>=2, hata olmayan koşular) kullanılır.
	BlockRepeatableTotal int
	BlockRepeatableCases int
	BlockRepeatablePct   float64
	// UnexpectedBlocks (#181): "pass" BEKLENEN (Expect/ExpectAny "pass"
	// içeren) kartlarda GERÇEKTE blok çıkan satır SAYISI — izleme VE error
	// satırları HARİÇ. PO'nun tuttuğu kartlarda kaç run yanlışlıkla
	// bloklamış — canlı/aday karşılaştırmasında kullanılır.
	UnexpectedBlocks int
	// WatchCases/WatchMatch: izleme satırları — uyuma girmez, ayrı raporlanır.
	WatchCases int
	WatchMatch int
	// ErrorRows: bu mercekte "error" verdict'i taşıyan TOPLAM satır sayısı
	// (#175 madde D/E — özette ayrıca bildirilir).
	ErrorRows int
	// Model: bu merceği koşturan istemcinin adı (ilk hata-olmayan satırdan
	// alınır — aynı koşuda hep aynı istemci kullanıldığından tekildir).
	Model string
}

// LensABResult, RunLensAB'nin dönüşü.
type LensABResult struct {
	// Rows: BU KOŞUDA üretilen YENİ satırlar (--resume'da ExistingRows
	// TEKRARLANMAZ — onlar zaten diskte).
	Rows []LensABRow
	// Summaries: --resume'da ExistingRows + Rows BİRLİKTE (eski+yeni TÜM
	// satırlar) üzerinden hesaplanır; aksi halde yalnız Rows.
	Summaries      []LensABLensSummary // ilk görülme sırasına göre
	TotalTokens    int
	BudgetExceeded bool
	// StoppedAtCase: bütçe aşıldığında durulan set indeksi (0-tabanlı,
	// filtrelenmiş set üzerinden); aşılmadıysa -1.
	StoppedAtCase int
	StoppedAtRun  int
}

// truncateReason, LensABRow.Reason alanını 200 karaktere (rune bazlı —
// çok baytlı TR karakterleri ortadan bölmemek için) kırpar (#175: "reason=
// hata metni (ilk 200 karakter)").
func truncateReason(s string) string {
	const limit = 200
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}

// lensABSleep, d>0 ise ctx iptaline duyarlı şekilde d kadar bekler; d<=0 ise
// hiçbir şey yapmaz. ctx iptal edilirse ctx.Err() döner — RunLensAB bunu
// GERÇEK bir iptal sayıp tüm koşuyu durdurur (bir "hata satırı" DEĞİL).
func lensABSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunLensAB, set'teki (opts.Lens'e göre filtrelenmiş) her çifti
// opts.PromptVersion (ya da opts.PromptText) sistem prompt'uyla, sıcaklık
// 0'da, opts.Runs kez çalıştırır. Hiçbir DB YAZIMI yapmaz. distinctChat
// doluysa (ilk eleman) "distinctiveness" mercek çağrıları ONUNLA yapılır —
// diğer tüm mercekler chat ile (üretimdeki seçimle AYNI, bkz.
// gate.evaluateDistinctiveness — ama üretimin "tek seferlik yedek deneme"si
// BURADA YOK, #175 madde D: yalnız birincil istemci ölçülür).
//
// llm.UsageMeter ile her (mercek, sürüm) aşaması ayrı etiketlenir
// (llm.WithStage) — token sayımı buradan okunur (yalnız BAŞARILI çağrılar
// sayılır, retry'ler DEĞİL).
//
// Oran sınırı (llm.IsRateLimited) DIŞI bir LLM hatası, girdi (DB) hatası ya
// da oran sınırı yeniden denemeleri (opts.RateRetries) tükenirse: o (çift,
// koşu) için verdict="error" satırı yazılır, KOŞU BİTİRİLMEZ (devam eder).
// Bütçe aşılınca kalan çiftler/koşular ÇAĞRILMAZ, StoppedAtCase/Run
// doldurulur.
func RunLensAB(ctx context.Context, st *store.Store, chat llm.Chat, set []GoldenCase, opts LensABOptions, distinctChat ...llm.Chat) (LensABResult, error) {
	runs := opts.Runs
	if runs <= 0 {
		runs = 1
	}

	lensFilter := opts.Lens
	if lensFilter == "" {
		lensFilter = "all"
	}
	if lensFilter != "all" {
		if _, ok := lensRegistry[lensFilter]; !ok {
			return LensABResult{}, fmt.Errorf("lens-ab: bilinmeyen mercek: %q", lensFilter)
		}
	}

	usingPromptText := opts.PromptText != ""
	promptCol := opts.PromptVersion
	if usingPromptText {
		// Bir aday prompt metni tüm dört merceğe AYNI anlama gelmez —
		// yalnız TEK mercekle ölçülür (main.go da aynı kısıtı erken
		// uygular, burası testte doğrudan çağrılan RunLensAB için).
		if lensFilter == "all" {
			return LensABResult{}, fmt.Errorf("lens-ab: --prompt-file yalnız tek mercekle (--lens=all İLE OLMAZ) kullanılabilir")
		}
		promptCol = opts.PromptLabel
		if promptCol == "" {
			promptCol = "file"
		}
	} else if opts.PromptVersion != "v1" && opts.PromptVersion != "v3" {
		return LensABResult{}, fmt.Errorf("lens-ab: --prompt v1|v3 olmalı, geldi: %q", opts.PromptVersion)
	}

	// Votes (#181): bir oylama kararı yalnız özgünlük merceği için
	// tanımlıdır — >1 iken --lens=distinctiveness DIŞINDA (özellikle "all")
	// net hata döner, sessizce yok sayılmaz. <=1 BUGÜNKÜ tek-çağrılık
	// davranışı DEĞİŞTİRMEZ (promptCol'a ek YAPILMAZ).
	votes := opts.Votes
	if votes <= 0 {
		votes = 1
	}
	if votes > 1 {
		if lensFilter != "distinctiveness" {
			return LensABResult{}, fmt.Errorf("lens-ab: --votes >1 yalnız --lens=distinctiveness ile kullanılabilir (geldi: --lens=%q)", lensFilter)
		}
		// CSV prompt etiketine oy sayısı eklenir ("v1+oy3", "file:x+oy3")
		// ki --resume farklı oy sayılarını karıştırmasın (#181).
		promptCol = fmt.Sprintf("%s+oy%d", promptCol, votes)
	}

	var distinctOverride llm.Chat
	if len(distinctChat) > 0 {
		distinctOverride = distinctChat[0]
	}

	cases := set
	if lensFilter != "all" {
		filtered := make([]GoldenCase, 0, len(set))
		for _, c := range set {
			if c.Lens == lensFilter {
				filtered = append(filtered, c)
			}
		}
		cases = filtered
	}

	meter := llm.NewUsageMeter()
	baseCtx := llm.WithMeter(ctx, meter)
	prevStageTotal := map[string]int{}

	result := LensABResult{StoppedAtCase: -1}

	order := []string{}
	orderSeen := map[string]bool{}

	emit := func(row LensABRow) error {
		result.Rows = append(result.Rows, row)
		result.TotalTokens += row.Tokens
		if opts.OnRow != nil {
			return opts.OnRow(row)
		}
		return nil
	}

outer:
	for ci, gc := range cases {
		def, ok := lensRegistry[gc.Lens]
		if !ok {
			return result, fmt.Errorf("lens-ab: altın set id=%d (%s) bilinmeyen mercek %q", gc.ID, gc.Kind, gc.Lens)
		}

		if !orderSeen[gc.Lens] {
			orderSeen[gc.Lens] = true
			order = append(order, gc.Lens)
		}

		// Bu çiftin TÜM koşuları --resume'da zaten tamamlanmışsa (CSV'de
		// hatasız satır olarak var) DB/LLM'e hiç gidilmez.
		allDone := true
		for run := 1; run <= runs; run++ {
			if !opts.SkipKeys[LensABRowKey{ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run}] {
				allDone = false
				break
			}
		}
		if allDone {
			continue
		}

		activeChat := chat
		if gc.Lens == "distinctiveness" && distinctOverride != nil {
			activeChat = distinctOverride
		}
		modelName := modelNameOf(activeChat)

		system := def.v1
		switch {
		case usingPromptText:
			system = opts.PromptText
		case opts.PromptVersion == "v3":
			system = def.v3
		}

		userPrompt, err := lensABUserPrompt(ctx, st, gc)
		if err != nil {
			// Girdi (DB) hatası: LLM'e hiç gidilmeden, henüz tamamlanmamış
			// HER koşu için bir "error" satırı yazılır, sonraki çifte
			// geçilir (#175 madde A).
			for run := 1; run <= runs; run++ {
				key := LensABRowKey{ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run}
				if opts.SkipKeys[key] {
					continue
				}
				row := LensABRow{
					ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run,
					Verdict: "error", Expect: expectLabel(gc), Watch: gc.Watch,
					Model: modelName, Reason: truncateReason(fmt.Sprintf("girdi hatası: %v", err)),
				}
				if werr := emit(row); werr != nil {
					return result, werr
				}
			}
			continue
		}

		for run := 1; run <= runs; run++ {
			key := LensABRowKey{ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run}
			if opts.SkipKeys[key] {
				continue
			}

			stage := gc.Lens + "/" + promptCol
			callCtx := llm.WithStage(baseCtx, stage)

			var row LensABRow
			if gc.Lens == "distinctiveness" && votes > 1 {
				// #181: N-oy yolu — üretimdeki SAF voteDistinctiveness
				// çekirdeğini (gate.go) KULLANIR, kopyalamaz.
				r, verr := voteDistinctivenessLensABRow(ctx, callCtx, activeChat, system, userPrompt, votes, opts, meter, stage, prevStageTotal, gc, promptCol, modelName, run)
				if verr != nil {
					return result, verr
				}
				row = r
			} else {
				var raw string
				var callErr error
				rateRetriesUsed := 0
				for {
					raw, callErr = activeChat.ChatJSONWithTemperature(callCtx, system, userPrompt, 0)
					if sleepErr := lensABSleep(ctx, opts.SleepAfterCall); sleepErr != nil {
						return result, sleepErr
					}
					if callErr == nil {
						break
					}
					if !llm.IsRateLimited(callErr) || rateRetriesUsed >= opts.RateRetries {
						break
					}
					rateRetriesUsed++
					if waitErr := lensABSleep(ctx, opts.RateWait); waitErr != nil {
						return result, waitErr
					}
				}

				if callErr != nil {
					// Oran sınırı DIŞI hata YA DA oran sınırı yeniden
					// denemeleri tükendi: koşu BİTİRİLMEZ, "error" satırı
					// yazılıp devam edilir (#175 madde A).
					row = LensABRow{
						ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run,
						Verdict: "error", Expect: expectLabel(gc), Watch: gc.Watch,
						Model: modelName, Reason: truncateReason(callErr.Error()),
					}
				} else {
					v := parseLensVerdict(raw)
					snap := meter.Snapshot()[stage]
					delta := snap.TotalTokens - prevStageTotal[stage]
					prevStageTotal[stage] = snap.TotalTokens

					row = LensABRow{
						ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run,
						Verdict: v.Verdict, Criterion: v.Criterion, Expect: expectLabel(gc),
						Match: matchGoldenCase(gc, v), Tokens: delta, Watch: gc.Watch,
						Model: modelName, Reason: truncateReason(v.Reason),
					}
				}
			}

			if werr := emit(row); werr != nil {
				return result, werr
			}

			if opts.BudgetTokens > 0 && result.TotalTokens >= opts.BudgetTokens {
				result.BudgetExceeded = true
				result.StoppedAtCase = ci
				result.StoppedAtRun = run
				break outer
			}
		}
	}

	allRows := result.Rows
	if len(opts.ExistingRows) > 0 {
		allRows = make([]LensABRow, 0, len(opts.ExistingRows)+len(result.Rows))
		allRows = append(allRows, opts.ExistingRows...)
		allRows = append(allRows, result.Rows...)
	}
	result.Summaries = buildLensSummaries(order, allRows)
	return result, nil
}

// voteDistinctivenessLensABRow (#181), --votes>1 iken TEK (çift, koşu)
// satırını üretir: n oy çağrısı yapılır (her biri kendi oran-sınırı
// bekle-yeniden-dene döngüsünü ve --sleep-ms'i İZLER — tek-çağrılık yoldaki
// döngünün AYNISI, oy başına tekrarlanır), sonra üretimdeki SAF çekirdek
// (gate.go voteDistinctiveness) kararı verir — iki kopya karar mantığı YOK.
// err yalnız GERÇEK bir ctx iptalinde (lensABSleep) dolu döner, RunLensAB'yi
// durdurur; bir LLM/oran-sınırı hatası err DEĞİL, decision.Err'e ya da
// (k>1'de) tartışmalı karara düşer — normal "error"/karar satırı üretir.
func voteDistinctivenessLensABRow(ctx, callCtx context.Context, activeChat llm.Chat, system, userPrompt string, n int, opts LensABOptions, meter *llm.UsageMeter, stage string, prevStageTotal map[string]int, gc GoldenCase, promptCol, modelName string, run int) (LensABRow, error) {
	tokens := 0
	var abort error

	call := func(vctx context.Context) (lensVerdict, string, error) {
		var raw string
		var callErr error
		rateRetriesUsed := 0
		for {
			raw, callErr = activeChat.ChatJSONWithTemperature(vctx, system, userPrompt, 0)
			if sleepErr := lensABSleep(ctx, opts.SleepAfterCall); sleepErr != nil {
				abort = sleepErr
				return lensVerdict{}, modelName, sleepErr
			}
			if callErr == nil {
				break
			}
			if !llm.IsRateLimited(callErr) || rateRetriesUsed >= opts.RateRetries {
				break
			}
			rateRetriesUsed++
			if waitErr := lensABSleep(ctx, opts.RateWait); waitErr != nil {
				abort = waitErr
				return lensVerdict{}, modelName, waitErr
			}
		}
		if callErr != nil {
			return lensVerdict{}, modelName, callErr
		}
		snap := meter.Snapshot()[stage]
		delta := snap.TotalTokens - prevStageTotal[stage]
		prevStageTotal[stage] = snap.TotalTokens
		tokens += delta
		return parseLensVerdict(raw), modelName, nil
	}

	decision, voteRecords := voteDistinctiveness(callCtx, n, call)
	if abort != nil {
		return LensABRow{}, abort
	}

	voteStrs := make([]string, len(voteRecords))
	for i, vr := range voteRecords {
		if vr.Err != nil {
			voteStrs[i] = "error"
			continue
		}
		voteStrs[i] = vr.Verdict.Verdict
	}
	votesLabel := strings.Join(voteStrs, ",")

	if decision.Err != nil {
		// k==1 hata — üretimdeki kural: normal "error" satırı (koşu devam
		// eder, bitmez, #175 madde A ile AYNI tutum).
		return LensABRow{
			ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run,
			Verdict: "error", Expect: expectLabel(gc), Watch: gc.Watch,
			Model: modelName, Tokens: tokens,
			Reason: truncateReason(fmt.Sprintf("oylar: %s — %v", votesLabel, decision.Err)),
		}, nil
	}

	v := lensVerdict{Verdict: decision.Verdict, Criterion: decision.Criterion, Reason: decision.Reason}
	return LensABRow{
		ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: promptCol, Run: run,
		Verdict: v.Verdict, Criterion: v.Criterion, Expect: expectLabel(gc),
		Match: matchGoldenCase(gc, v), Tokens: tokens, Watch: gc.Watch,
		Model: modelName, Reason: truncateReason(fmt.Sprintf("oylar: %s — %s", votesLabel, decision.Reason)),
	}, nil
}

// lensABUserPrompt, GoldenCase.Kind'e göre kullanıcı promptunu DB'den kurar.
// idea: ideaLensUserPrompt (mevcut mercek girdisiyle BİREBİR, #123). elimination:
// aynı şablon — subject eliminations.subject'e (title yerine), detail
// eliminations.detail'e (problem yerine) yazılır; solution/target_user boş
// (eliminations bu alanları taşımaz).
func lensABUserPrompt(ctx context.Context, st *store.Store, gc GoldenCase) (string, error) {
	switch gc.Kind {
	case "idea":
		idea, err := st.GetIdeaForAudit(ctx, gc.ID)
		if err != nil {
			return "", err
		}
		return ideaLensUserPrompt(idea.Title, idea.ProblemStatement, idea.ProposedSolution, idea.TargetUser), nil
	case "elimination":
		e, err := st.GetElimination(ctx, gc.ID)
		if err != nil {
			return "", err
		}
		detail := ""
		if e.Detail != nil {
			detail = *e.Detail
		}
		return ideaLensUserPrompt(e.Subject, detail, "", ""), nil
	default:
		return "", fmt.Errorf("bilinmeyen kind: %q", gc.Kind)
	}
}

// expectLabel, CSV'nin "expect" kolonu — ExpectAny doluysa "pass|unsure"
// biçiminde, aksi halde tek Expect.
func expectLabel(gc GoldenCase) string {
	if len(gc.ExpectAny) > 0 {
		return strings.Join(gc.ExpectAny, "|")
	}
	return gc.Expect
}

// matchGoldenCase, mercek cevabının altın set beklentisiyle uyup uymadığını
// belirler: verdict Expect/ExpectAny kümesinde olmalı; distinctiveness'ta
// "fail" bekleniyorsa VE Criterion doluysa, dönen criterion da eşleşmeli
// (yanlış K etiketi uyumsuzluk sayılır — #165 kabul eşiği).
func matchGoldenCase(gc GoldenCase, v lensVerdict) bool {
	expects := gc.ExpectAny
	if len(expects) == 0 {
		if gc.Expect == "" {
			return false
		}
		expects = []string{gc.Expect}
	}
	ok := false
	for _, e := range expects {
		if e == v.Verdict {
			ok = true
			break
		}
	}
	if !ok {
		return false
	}
	if gc.Criterion != "" && v.Verdict == "fail" && v.Criterion != gc.Criterion {
		return false
	}
	return true
}

// buildLensSummaries, satırları mercek+id+kind bazında gruplayıp özet
// istatistikleri üretir. order, mercek adlarının ilk görülme sırasıdır.
// "error" verdict'li satırlar uyum (Cases/Matches) ve tekrarlanabilirlik
// (RepeatableTotal/RepeatableCases) paydalarından HARİÇ tutulur (#175
// madde E) — ayrıca ErrorRows'a sayılır.
func buildLensSummaries(order []string, rows []LensABRow) []LensABLensSummary {
	type key struct {
		lens string
		id   int64
		kind string
	}
	summaries := make(map[string]*LensABLensSummary, len(order))
	for _, lens := range order {
		summaries[lens] = &LensABLensSummary{Lens: lens}
	}
	groups := map[key][]LensABRow{}
	groupOrder := []key{}

	for _, r := range rows {
		if s, ok := summaries[r.Lens]; ok {
			s.TotalTokens += r.Tokens
			if r.Verdict == "error" {
				s.ErrorRows++
			}
			if s.Model == "" && r.Model != "" {
				s.Model = r.Model
			}
			// UnexpectedBlocks (#181): "pass" beklenen (PO'nun tuttuğu)
			// kartlarda GERÇEKTE blok çıkan SATIR sayısı — izleme VE error
			// satırları HARİÇ.
			if !r.Watch && r.Verdict != "error" && expectsPass(r.Expect) && isBlockVerdict(r) {
				s.UnexpectedBlocks++
			}
		}
		k := key{r.Lens, r.ID, r.Kind}
		if _, ok := groups[k]; !ok {
			groupOrder = append(groupOrder, k)
		}
		groups[k] = append(groups[k], r)
	}

	for _, k := range groupOrder {
		grp := groups[k]
		s, ok := summaries[k.lens]
		if !ok {
			continue
		}

		// first: run==1 VE hata OLMAYAN satır — uyum yalnız BAŞARILI ilk
		// koşuya bakar; run==1 hata verdiyse bu çift Cases/Matches
		// paydasına HİÇ girmez (#175 madde E).
		var first *LensABRow
		for i := range grp {
			if grp[i].Run == 1 && grp[i].Verdict != "error" {
				r := grp[i]
				first = &r
				break
			}
		}

		// Tekrarlanabilirlik yalnız HATA OLMAYAN koşular üzerinden
		// hesaplanır (hatalı koşular karşılaştırma dışı).
		nonError := make([]LensABRow, 0, len(grp))
		for _, r := range grp {
			if r.Verdict != "error" {
				nonError = append(nonError, r)
			}
		}
		allSame := true
		for i := 1; i < len(nonError); i++ {
			if nonError[i].Verdict != nonError[0].Verdict {
				allSame = false
			}
		}

		watch := len(grp) > 0 && grp[0].Watch
		if watch {
			s.WatchCases++
			if first != nil && first.Match {
				s.WatchMatch++
			}
			continue
		}
		if first != nil {
			s.Cases++
			if first.Match {
				s.Matches++
			}
		}
		if len(nonError) > 1 {
			s.RepeatableTotal++
			if allSame {
				s.RepeatableCases++
			}

			// BlockRepeatableTotal/Cases (#181): AYNI payda (Runs>=2, hata
			// olmayan koşular), ama "blok mu değil mi" kararı — K1 fail vs
			// K2 fail FARKLI verdict/criterion (allSame=false olabilir) ama
			// ikisi de "blok", burada AYNI sayılır.
			blockSame := true
			firstBlock := isBlockVerdict(nonError[0])
			for i := 1; i < len(nonError); i++ {
				if isBlockVerdict(nonError[i]) != firstBlock {
					blockSame = false
					break
				}
			}
			s.BlockRepeatableTotal++
			if blockSame {
				s.BlockRepeatableCases++
			}
		}
	}

	out := make([]LensABLensSummary, 0, len(order))
	for _, lens := range order {
		s := summaries[lens]
		if s.Cases > 0 {
			s.MatchPct = 100 * float64(s.Matches) / float64(s.Cases)
		}
		if s.RepeatableTotal > 0 {
			s.RepeatablePct = 100 * float64(s.RepeatableCases) / float64(s.RepeatableTotal)
		}
		if s.BlockRepeatableTotal > 0 {
			s.BlockRepeatablePct = 100 * float64(s.BlockRepeatableCases) / float64(s.BlockRepeatableTotal)
		}
		out = append(out, *s)
	}
	return out
}

// isBlockVerdict, bir LensABRow'un "blok" sayılıp sayılmayacağını bildirir
// (#181): distinctiveness'ta yalnız fail&(K1|K2) blok (K3/K4 fail blok
// SAYILMAZ — voteDistinctiveness'in "blok oyu" tanımıyla AYNI, gate.go),
// diğer merceklerde herhangi bir "fail" blok sayılır (tek bloklayıcı mercek
// grubu, ilk/tüm-fail ayrımı önemsiz — burada yalnız SONUÇ karşılaştırılır).
func isBlockVerdict(r LensABRow) bool {
	if r.Verdict != "fail" {
		return false
	}
	if r.Lens == "distinctiveness" {
		return r.Criterion == "K1" || r.Criterion == "K2"
	}
	return true
}

// expectsPass, bir LensABRow.Expect etiketinin ("pass", "fail",
// "pass|unsure" gibi expectLabel çıktıları) "pass"ı KABUL EDİLEBİLİR
// bulup bulmadığını bildirir (#181: "pass beklenen kartlar" — ExpectAny
// "pass" içeren satırlar da dahil).
func expectsPass(expect string) bool {
	for _, e := range strings.Split(expect, "|") {
		if e == "pass" {
			return true
		}
	}
	return false
}

// lensABCSVHeader, WriteLensABCSV'nin ürettiği kolon sırası (#175: "kind" ve
// "model" eklendi, "reason" en sona eklendi — id, kind, lens, prompt, run,
// verdict, criterion, expect, match, tokens, model, reason).
var lensABCSVHeader = []string{"id", "kind", "lens", "prompt", "run", "verdict", "criterion", "expect", "match", "tokens", "model", "reason"}

// WriteLensABCSVHeader, yalnız başlık satırını yazar — main.go'nun anında
// yazım akışında (satırlar WriteLensABCSVRow ile AYRI yazılır) dosyanın
// başında BİR KEZ çağrılır; --resume'da var olan dosyaya eklenirken
// ATLANIR (#175 madde B).
func WriteLensABCSVHeader(cw *csv.Writer) error {
	return cw.Write(lensABCSVHeader)
}

// WriteLensABCSVRow, TEK satırı yazar — main.go bunu her satır üretildiğinde
// çağırıp hemen Flush eder (#175 madde B: süreç ortada ölse de o ana
// kadarki satırlar dosyada kalır).
func WriteLensABCSVRow(cw *csv.Writer, r LensABRow) error {
	return cw.Write([]string{
		strconv.FormatInt(r.ID, 10),
		r.Kind,
		r.Lens,
		r.PromptVersion,
		strconv.Itoa(r.Run),
		r.Verdict,
		r.Criterion,
		r.Expect,
		strconv.FormatBool(r.Match),
		strconv.Itoa(r.Tokens),
		r.Model,
		r.Reason,
	})
}

// WriteLensABCSV, sonuç satırlarını sözleşmedeki kolonlarla TEK seferde
// yazar (testler ve tek seferlik/toplu kullanım için — cmdLensAB artık
// WriteLensABCSVHeader + satır satır WriteLensABCSVRow+Flush kullanıyor,
// #175 madde B).
func WriteLensABCSV(w io.Writer, rows []LensABRow) error {
	cw := csv.NewWriter(w)
	if err := WriteLensABCSVHeader(cw); err != nil {
		return err
	}
	for _, r := range rows {
		if err := WriteLensABCSVRow(cw, r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// parseLensABCSVRow, LoadLensABResumeState'in tek bir veri satırını (başlık
// HARİÇ) LensABRow'a geri çevirir — sütun sırası lensABCSVHeader ile BİREBİR.
// Watch burada DOLDURULMAZ (CSV'de yok) — çağıran altın settem eşleştirir.
func parseLensABCSVRow(rec []string) (LensABRow, error) {
	if len(rec) != len(lensABCSVHeader) {
		return LensABRow{}, fmt.Errorf("sütun sayısı %d, beklenen %d", len(rec), len(lensABCSVHeader))
	}
	id, err := strconv.ParseInt(rec[0], 10, 64)
	if err != nil {
		return LensABRow{}, fmt.Errorf("id: %w", err)
	}
	run, err := strconv.Atoi(rec[4])
	if err != nil {
		return LensABRow{}, fmt.Errorf("run: %w", err)
	}
	match, err := strconv.ParseBool(rec[8])
	if err != nil {
		return LensABRow{}, fmt.Errorf("match: %w", err)
	}
	tokens, err := strconv.Atoi(rec[9])
	if err != nil {
		return LensABRow{}, fmt.Errorf("tokens: %w", err)
	}
	return LensABRow{
		ID: id, Kind: rec[1], Lens: rec[2], PromptVersion: rec[3], Run: run,
		Verdict: rec[5], Criterion: rec[6], Expect: rec[7], Match: match, Tokens: tokens,
		Model: rec[10], Reason: rec[11],
	}, nil
}

// lensABHeadersEqual, iki dilimin sırayla AYNI olup olmadığını bildirir (CSV
// başlığı karşılaştırması için — encoding/csv sonucu her zaman []string).
// ingest_test.go'daki AYNI amaçlı equalStrings'le İSİM ÇAKIŞMASINI önlemek
// için ayrı adlandırıldı.
func lensABHeadersEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LoadLensABResumeState, --resume için VAR OLAN CSV çıktısını okur (#175):
// her satırı LensABRow'a geri çevirir (Watch, set'teki eşleşen GoldenCase'in
// Watch alanından kurulur — CSV bunu taşımaz), verdict'i "error" OLMAYAN
// satırların LensABRowKey'ini skip kümesine ekler (bu koşularda LLM TEKRAR
// ÇAĞRILMAZ). path yoksa (henüz üretilmemiş dosya) ya da tamamen boşsa
// hata VERMEDEN boş durumla döner — ilk --resume koşusu böyle başlar.
// Başlık lensABCSVHeader'la BİREBİR uyuşmuyorsa net hata döner.
func LoadLensABResumeState(path string, set []GoldenCase) ([]LensABRow, map[LensABRowKey]bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, map[LensABRowKey]bool{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lens-ab --resume: CSV açılamadı: %w", err)
	}
	defer f.Close()

	cr := csv.NewReader(f)
	header, err := cr.Read()
	if err == io.EOF {
		return nil, map[LensABRowKey]bool{}, nil // tamamen boş dosya
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lens-ab --resume: CSV başlığı okunamadı: %w", err)
	}
	if !lensABHeadersEqual(header, lensABCSVHeader) {
		return nil, nil, fmt.Errorf("lens-ab --resume: CSV başlığı beklenenle uyuşmuyor:\n  geldi: %v\n  beklenen: %v", header, lensABCSVHeader)
	}

	type caseIdent struct {
		id   int64
		kind string
		lens string
	}
	watchLookup := make(map[caseIdent]bool, len(set))
	for _, gc := range set {
		watchLookup[caseIdent{gc.ID, gc.Kind, gc.Lens}] = gc.Watch
	}

	var rows []LensABRow
	skip := map[LensABRowKey]bool{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("lens-ab --resume: CSV satırı okunamadı: %w", err)
		}
		row, err := parseLensABCSVRow(rec)
		if err != nil {
			return nil, nil, fmt.Errorf("lens-ab --resume: CSV satırı ayrıştırılamadı (%v): %w", rec, err)
		}
		row.Watch = watchLookup[caseIdent{row.ID, row.Kind, row.Lens}]
		rows = append(rows, row)
		if row.Verdict != "error" {
			skip[LensABRowKey{ID: row.ID, Kind: row.Kind, Lens: row.Lens, PromptVersion: row.PromptVersion, Run: row.Run}] = true
		}
	}
	return rows, skip, nil
}

// FormatLensABSummary, mercek başına uyum/tekrarlanabilirlik/token/model/
// hata özetini + izleme listesini + bütçe durma notunu TR metin olarak
// üretir (stderr'e basılır, CSV'ye karışmaz).
func FormatLensABSummary(result LensABResult) string {
	var sb strings.Builder
	for _, s := range result.Summaries {
		fmt.Fprintf(&sb, "%s: uyum %d/%d (%.0f%%)", s.Lens, s.Matches, s.Cases, s.MatchPct)
		if s.RepeatableTotal > 0 {
			fmt.Fprintf(&sb, ", tekrarlanabilirlik %d/%d (%.0f%%)", s.RepeatableCases, s.RepeatableTotal, s.RepeatablePct)
		}
		if s.BlockRepeatableTotal > 0 {
			fmt.Fprintf(&sb, ", blok tekrarlanabilirliği %d/%d (%.0f%%)", s.BlockRepeatableCases, s.BlockRepeatableTotal, s.BlockRepeatablePct)
		}
		fmt.Fprintf(&sb, ", token %d", s.TotalTokens)
		if s.Model != "" {
			fmt.Fprintf(&sb, ", model %s", s.Model)
		}
		if s.ErrorRows > 0 {
			fmt.Fprintf(&sb, ", hata %d satır", s.ErrorRows)
		}
		if s.UnexpectedBlocks > 0 {
			fmt.Fprintf(&sb, ", pass beklenen kartlarda blok: %d satır", s.UnexpectedBlocks)
		}
		if s.WatchCases > 0 {
			fmt.Fprintf(&sb, " — izleme %d/%d beklendiği gibi", s.WatchMatch, s.WatchCases)
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "toplam token: %d\n", result.TotalTokens)
	if result.BudgetExceeded {
		fmt.Fprintf(&sb, "bütçe aşıldı — durduğu yer: set indeksi %d, koşu %d\n", result.StoppedAtCase, result.StoppedAtRun)
	}
	return sb.String()
}
