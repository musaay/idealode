package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/store"
)

// --- #212: tohum kartı istemi v4 + özgünlük v6 (büyüme istisnası) ---

// TestSeedCardPromptsV4: SEED_CARD_PROMPT=v4 -> V4 sistem + v3 etiketi (gelir);
// ivme tohumu v1 yolunda kalır; v3 yolu aynı (kabul 1).
func TestSeedCardPromptsV4(t *testing.T) {
	seed := radarSeed{Name: "N", Summary: "S", Evidence: "E", SourceURL: "https://x.example", TRAngle: "TR açısı"}
	gate := trendingGateResult{repo: "o/r", stars: 100, lastDelta: 5, persistedDays: 3}
	cfg := &config.Config{OutputLang: "tr", SeedCardPrompt: "v4"}

	sys, usr := seedCardPrompts(cfg, seed, false, gate)
	if sys != fmt.Sprintf(seedCardSystemTmplV4, langName("tr")) {
		t.Error("v4 gelir sistem istemi V4 şablonu olmalı")
	}
	if usr != "Seed name: N\nSummary: S\nRevenue/traction evidence: E\nSource: https://x.example" {
		t.Errorf("v4 kullanıcı istemi açısız olmalı, geldi: %q", usr)
	}
	if strings.Contains(usr, "Seed angle") || strings.Contains(usr, seed.TRAngle) {
		t.Error("v4 kullanıcı istemi açı etiketi/metni taşımamalı")
	}
	if sys == fmt.Sprintf(seedCardSystemTmplV3, langName("tr")) {
		t.Error("v4 sistem istemi v3'ten farklı olmalı")
	}

	// İvme: v4'te de v1 yolu.
	sys, usr = seedCardPrompts(cfg, seed, true, gate)
	if sys != fmt.Sprintf(seedMomentumCardSystemTmpl, langName("tr")) ||
		usr != seedMomentumCardUserPrompt(seed, "o/r", 100, 5, 3) {
		t.Error("v4'te ivme istemi v1 yolunda kalmalı")
	}

	// v3 hâlâ V3 (v4 eklenmesi onu bozmadı).
	cfg.SeedCardPrompt = "v3"
	sys, usr = seedCardPrompts(cfg, seed, false, gate)
	if sys != fmt.Sprintf(seedCardSystemTmplV3, langName("tr")) || usr != seedCardUserPromptLabeled(seed, seedAngleLabelV3) {
		t.Error("v3 yolu değişmemeli")
	}
}

// TestSeedCardSystemTmplV4Placeholder: V4 şablonunda tam bir %s (langName) var;
// Sprintf bozuk biçim üretmez.
func TestSeedCardSystemTmplV4Placeholder(t *testing.T) {
	if n := strings.Count(seedCardSystemTmplV4, "%"); n != 1 || strings.Count(seedCardSystemTmplV4, "%s") != 1 {
		t.Fatalf("V4 şablonunda tam bir %%s beklenirdi, yüzde sayısı: %d", n)
	}
	if got := fmt.Sprintf(seedCardSystemTmplV4, "Turkish"); strings.Contains(got, "%!") || !strings.Contains(got, "MUST be written in Turkish.") {
		t.Errorf("Sprintf sonucu bozuk: %q", got)
	}
}

// distinctCaptureChat, özgünlük çağrısının (system, user) çiftini yakalar.
type distinctCaptureChat struct {
	system, user string
}

func (c *distinctCaptureChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *distinctCaptureChat) ChatJSONWithTemperature(_ context.Context, system, user string, _ float64) (string, error) {
	c.system, c.user = system, user
	return `{"verdict":"pass","criterion":"none","reason":"ok"}`, nil
}

func distinctIdeaFor(sourceType string, quotes []string) *store.Idea {
	return &store.Idea{
		Title: "T", ProblemStatement: "P", ProposedSolution: "S", TargetUser: "U",
		SourceType: sourceType, ExampleQuotes: quotes,
	}
}

// TestDistinctivenessDefaultByteIdentical (kabul 2): sürüm yok / "v4" / geçersiz
// ("v5" dahil) -> sistem lensDistinctivenessSystem, kullanıcı istemi bugünkü
// biçimle BYTE-BYTE aynı; TÜM kaynak türlerinde, dolu alıntıda bile.
func TestDistinctivenessDefaultByteIdentical(t *testing.T) {
	const wantUser = "Idea title: T\nProblem: P\nSolution: S\nTarget user: U"
	quotes := []string{"Kanıt (X): $10K MRR — https://x.example"}
	types := []string{"market_derived", "pain_point", "momentum_derived", ""}
	ctxs := map[string]context.Context{
		"ctx-yok": context.Background(),
		"v4":      withDistinctivenessPrompt(context.Background(), "v4"),
		"v5":      withDistinctivenessPrompt(context.Background(), "v5"),
		"bos":     withDistinctivenessPrompt(context.Background(), ""),
	}
	for name, ctx := range ctxs {
		for _, st := range types {
			chat := &distinctCaptureChat{}
			if _, err := distinctivenessRaw(ctx, chat, distinctIdeaFor(st, quotes)); err != nil {
				t.Fatalf("%s/%s: %v", name, st, err)
			}
			if chat.system != lensDistinctivenessSystem {
				t.Errorf("%s/%s: sistem istemi v4'ten farklı", name, st)
			}
			if chat.user != wantUser {
				t.Errorf("%s/%s: kullanıcı istemi bugünküyle aynı değil: %q", name, st, chat.user)
			}
		}
	}
}

// TestDistinctivenessV6: v6 -> V6 sistem; "Seed evidence" satırı YALNIZ
// market_derived + dolu ExampleQuotes'ta (kabul 2).
func TestDistinctivenessV6(t *testing.T) {
	const base = "Idea title: T\nProblem: P\nSolution: S\nTarget user: U"
	quotes := []string{"Kanıt (X): $10K MRR — https://x.example", "ikinci"}
	ctx := withDistinctivenessPrompt(context.Background(), "v6")

	cases := []struct {
		name, st string
		quotes   []string
		want     string
	}{
		{"market_derived dolu", "market_derived", quotes,
			base + "\nSeed evidence (verified revenue of a product in this market): Kanıt (X): $10K MRR — https://x.example"},
		{"market_derived boş alıntı", "market_derived", nil, base},
		{"market_derived boş dilim", "market_derived", []string{}, base},
		{"pain_point", "pain_point", quotes, base},
		{"momentum_derived", "momentum_derived", quotes, base},
		{"tür boş", "", quotes, base},
	}
	for _, tc := range cases {
		chat := &distinctCaptureChat{}
		if _, err := distinctivenessRaw(ctx, chat, distinctIdeaFor(tc.st, tc.quotes)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if chat.system != lensDistinctivenessSystemV6 {
			t.Errorf("%s: sistem istemi V6 olmalı", tc.name)
		}
		if chat.user != tc.want {
			t.Errorf("%s: kullanıcı istemi\n got: %q\nwant: %q", tc.name, chat.user, tc.want)
		}
	}
}

// TestDistinctivenessV6OnlyAddsParagraphD: V6 = v4 + yalnız (d) paragrafı,
// K1 (c)'den hemen sonra; başka satır değişmez.
func TestDistinctivenessV6OnlyAddsParagraphD(t *testing.T) {
	i := strings.Index(lensDistinctivenessSystemV6, "(d) Growing-market exception:")
	if i < 0 {
		t.Fatal("V6'da (d) paragrafı yok")
	}
	j := strings.Index(lensDistinctivenessSystemV6[i:], "\n")
	para := lensDistinctivenessSystemV6[i : i+j+1]
	if got := strings.Replace(lensDistinctivenessSystemV6, para, "", 1); got != lensDistinctivenessSystem {
		t.Error("(d) çıkarılınca V6, v4 metnine eşit olmalı")
	}
	// (d), (c)'den hemen sonra gelir.
	c := strings.Index(lensDistinctivenessSystemV6, "(c) The idea's only differences")
	cEnd := c + strings.Index(lensDistinctivenessSystemV6[c:], "\n") + 1
	if cEnd != i {
		t.Error("(d), (c)'den hemen sonra olmalı")
	}
	if !strings.Contains(lensDistinctivenessSystemV6, `a "Seed evidence" line`) {
		t.Error("(d) metni beklenen ifadeyi içermeli")
	}
}

// TestDistinctivenessVerdictVersion: lens_verdicts.prompt_version v6'da "v6",
// varsayılanda "v4" (evaluateDistinctiveness imzası değişmeden, ctx üzerinden).
func TestDistinctivenessVerdictVersion(t *testing.T) {
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{
		{context.Background(), "v4"},
		{withDistinctivenessPrompt(context.Background(), "v4"), "v4"},
		{withDistinctivenessPrompt(context.Background(), "v6"), "v6"},
	} {
		out := evaluateDistinctiveness(tc.ctx, &distinctCaptureChat{}, distinctIdeaFor("market_derived", []string{"e"}), 1)
		if len(out.Verdicts) != 1 || out.Verdicts[0].PromptVersion != tc.want {
			t.Errorf("prompt_version %q beklenirdi, geldi: %+v", tc.want, out.Verdicts)
		}
	}
}

// TestDistinctivenessUserPromptHelpers: lens-ab'nin kullandığı ortak fonksiyon
// (v6) ile üretim yolu aynı çıktıyı verir; v4 değişmez.
func TestDistinctivenessUserPromptHelpers(t *testing.T) {
	idea := distinctIdeaFor("market_derived", []string{"Q"})
	base := ideaLensUserPrompt("T", "P", "S", "U")
	if got := distinctivenessUserPrompt("v4", idea); got != base {
		t.Errorf("v4 istemi değişmemeli: %q", got)
	}
	if got := distinctivenessUserPrompt("v6", idea); got != base+"\nSeed evidence (verified revenue of a product in this market): Q" {
		t.Errorf("v6 istemi beklenenden farklı: %q", got)
	}
}

// TestRunLensABSeedEvidenceOnlyDistinctiveness (kabul 4): --seed-evidence
// distinctiveness DIŞINDAki mercekle hata verir (DB'ye gitmeden).
func TestRunLensABSeedEvidenceOnlyDistinctiveness(t *testing.T) {
	for _, lens := range []string{"all", "third_party", "data_access", "market_viability", ""} {
		_, err := RunLensAB(context.Background(), nil, &distinctCaptureChat{}, nil,
			LensABOptions{Lens: lens, PromptVersion: "v1", SeedEvidence: true})
		if err == nil || !strings.Contains(err.Error(), "--seed-evidence") {
			t.Errorf("lens=%q: --seed-evidence hatası beklenirdi, geldi: %v", lens, err)
		}
	}
}
