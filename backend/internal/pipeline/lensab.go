package pipeline

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/musaay/idealode/backend/internal/llm"
	"github.com/musaay/idealode/backend/internal/store"
)

// lensab.go (#165, üst plan #163 §5/§6.2): `idealode lens-ab` komutunun
// gövdesi — altın set üzerinde v1/v3 mercek prompt'larını canlı Groq'a karşı
// karşılaştırır. HİÇBİR ŞEY DB'ye/eliminations'a YAZMAZ (yalnız
// GetIdeaForAudit/GetElimination ile OKUR); mevcut mercekleri
// (synthesize.go/seeds.go/gate.go) hiçbir şekilde ÇAĞIRMAZ ya da
// ETKİLEMEZ — ayrı, salt-ölçüm bir çağrı yoludur.

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

// LensABOptions, RunLensAB'nin çalışma parametreleri (idealode lens-ab
// bayraklarının doğrudan karşılığı).
type LensABOptions struct {
	// Lens: kanonik mercek kimliği (lensRegistry anahtarı) ya da "all"
	// (varsayılan — set'teki her satır kendi Lens'iyle koşar).
	Lens string
	// PromptVersion: "v1" ya da "v3" — set'teki HER satır için AYNI sürüm
	// kullanılır (A/B karşılaştırması iki ayrı komut koşusuyla yapılır).
	PromptVersion string
	// Runs: çift başına koşu sayısı — tekrarlanabilirlik için >=2 (#165
	// §5 kabul eşiği: "iki koşuda aynı verdict"). <=0 ise 1 sayılır.
	Runs int
	// BudgetTokens: birikimli token bu değere ULAŞTIĞINDA/AŞTIĞINDA durur
	// (kaldığı çift+koşu StoppedAtCase/StoppedAtRun'a yazılır). <=0 =
	// sınırsız.
	BudgetTokens int
}

// LensABRow, CSV'nin/raporun tek satırı — bir (çift, koşu) sonucudur.
type LensABRow struct {
	ID            int64
	Kind          string // idea | elimination — CSV'ye yazılmaz, iç gruplama için
	Lens          string
	PromptVersion string
	Run           int
	Verdict       string
	Criterion     string
	Expect        string
	Match         bool
	Tokens        int
	Watch         bool // CSV'ye yazılmaz — özet raporunda ayrı sayılır
	Error         string
}

// LensABLensSummary, mercek başına özet (rapor sonunda).
type LensABLensSummary struct {
	Lens string
	// Cases/Matches: İZLEME HARİÇ çiftler üzerinden, run=1'in Match'ine göre.
	Cases    int
	Matches  int
	MatchPct float64
	// RepeatableTotal/RepeatableCases: yalnız Runs>=2 koşulan çiftler
	// üzerinden — tüm koşularda AYNI verdict çıkanların oranı.
	RepeatableTotal int
	RepeatableCases int
	RepeatablePct   float64
	TotalTokens     int
	// WatchCases/WatchMatch: izleme satırları — uyuma girmez, ayrı raporlanır.
	WatchCases int
	WatchMatch int
}

// LensABResult, RunLensAB'nin dönüşü.
type LensABResult struct {
	Rows           []LensABRow
	Summaries      []LensABLensSummary // ilk görülme sırasına göre
	TotalTokens    int
	BudgetExceeded bool
	// StoppedAtCase: bütçe aşıldığında durulan set indeksi (0-tabanlı,
	// filtrelenmiş set üzerinden); aşılmadıysa -1.
	StoppedAtCase int
	StoppedAtRun  int
}

// RunLensAB, set'teki (opts.Lens'e göre filtrelenmiş) her çifti
// opts.PromptVersion sistem prompt'uyla, sıcaklık 0'da, opts.Runs kez
// çalıştırır. Hiçbir DB YAZIMI yapmaz. llm.UsageMeter ile her (mercek,
// sürüm) aşaması ayrı etiketlenir (llm.WithStage) — token sayımı buradan
// okunur, testte gerçek istemci + httptest sunucusuyla doğrulanır (sahte
// el-yazımı Chat'ler recordUsage'ı tetiklemez, bkz. usage_stage_test.go).
// Bütçe aşılınca kalan çiftler/koşular ÇAĞRILMAZ, StoppedAtCase/Run
// doldurulur.
func RunLensAB(ctx context.Context, st *store.Store, chat llm.Chat, set []GoldenCase, opts LensABOptions) (LensABResult, error) {
	runs := opts.Runs
	if runs <= 0 {
		runs = 1
	}
	if opts.PromptVersion != "v1" && opts.PromptVersion != "v3" {
		return LensABResult{}, fmt.Errorf("lens-ab: --prompt v1|v3 olmalı, geldi: %q", opts.PromptVersion)
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

outer:
	for ci, gc := range cases {
		def, ok := lensRegistry[gc.Lens]
		if !ok {
			return result, fmt.Errorf("lens-ab: altın set id=%d (%s) bilinmeyen mercek %q", gc.ID, gc.Kind, gc.Lens)
		}
		system := def.v1
		if opts.PromptVersion == "v3" {
			system = def.v3
		}

		userPrompt, err := lensABUserPrompt(ctx, st, gc)
		if err != nil {
			return result, fmt.Errorf("lens-ab: altın set id=%d (%s) girdi HATA: %w", gc.ID, gc.Kind, err)
		}

		if !orderSeen[gc.Lens] {
			orderSeen[gc.Lens] = true
			order = append(order, gc.Lens)
		}

		for run := 1; run <= runs; run++ {
			stage := gc.Lens + "/" + opts.PromptVersion
			callCtx := llm.WithStage(baseCtx, stage)
			raw, err := chat.ChatJSONWithTemperature(callCtx, system, userPrompt, 0)
			if err != nil {
				return result, fmt.Errorf("lens-ab: altın set id=%d (%s) run=%d: LLM HATA: %w", gc.ID, gc.Kind, run, err)
			}
			v := parseLensVerdict(raw)

			snap := meter.Snapshot()[stage]
			delta := snap.TotalTokens - prevStageTotal[stage]
			prevStageTotal[stage] = snap.TotalTokens

			row := LensABRow{
				ID: gc.ID, Kind: gc.Kind, Lens: gc.Lens, PromptVersion: opts.PromptVersion, Run: run,
				Verdict: v.Verdict, Criterion: v.Criterion, Expect: expectLabel(gc),
				Match: matchGoldenCase(gc, v), Tokens: delta, Watch: gc.Watch,
			}
			result.Rows = append(result.Rows, row)
			result.TotalTokens += delta

			if opts.BudgetTokens > 0 && result.TotalTokens >= opts.BudgetTokens {
				result.BudgetExceeded = true
				result.StoppedAtCase = ci
				result.StoppedAtRun = run
				break outer
			}
		}
	}

	result.Summaries = buildLensSummaries(order, result.Rows)
	return result, nil
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
		var first *LensABRow
		allSame := true
		for i := range grp {
			if grp[i].Run == 1 {
				r := grp[i]
				first = &r
			}
			if grp[i].Verdict != grp[0].Verdict {
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
		s.Cases++
		if first != nil && first.Match {
			s.Matches++
		}
		if len(grp) > 1 {
			s.RepeatableTotal++
			if allSame {
				s.RepeatableCases++
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
		out = append(out, *s)
	}
	return out
}

// lensABCSVHeader, WriteLensABCSV'nin ürettiği kolon sırası (#165 spec:
// "id, lens, prompt, run, verdict, criterion, expect, match, tokens").
var lensABCSVHeader = []string{"id", "lens", "prompt", "run", "verdict", "criterion", "expect", "match", "tokens"}

// WriteLensABCSV, sonuç satırlarını sözleşmedeki kolonlarla yazar.
func WriteLensABCSV(w io.Writer, rows []LensABRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(lensABCSVHeader); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			strconv.FormatInt(r.ID, 10),
			r.Lens,
			r.PromptVersion,
			strconv.Itoa(r.Run),
			r.Verdict,
			r.Criterion,
			r.Expect,
			strconv.FormatBool(r.Match),
			strconv.Itoa(r.Tokens),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// FormatLensABSummary, mercek başına uyum/tekrarlanabilirlik/token
// özetini + izleme listesini + bütçe durma notunu TR metin olarak üretir
// (stderr'e basılır, CSV'ye karışmaz).
func FormatLensABSummary(result LensABResult) string {
	var sb strings.Builder
	for _, s := range result.Summaries {
		fmt.Fprintf(&sb, "%s: uyum %d/%d (%.0f%%)", s.Lens, s.Matches, s.Cases, s.MatchPct)
		if s.RepeatableTotal > 0 {
			fmt.Fprintf(&sb, ", tekrarlanabilirlik %d/%d (%.0f%%)", s.RepeatableCases, s.RepeatableTotal, s.RepeatablePct)
		}
		fmt.Fprintf(&sb, ", token %d", s.TotalTokens)
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
