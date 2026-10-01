package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/backend/internal/config"
	"github.com/musaay/idealode/backend/internal/store"
)

// synthesize_thirdparty_test.go (#197): SynthesizeIdeas'ın (organik yol,
// stopOnFirstFail=true) THIRD_PARTY_PROMPT=v3 + THIRD_PARTY_VOTES=3
// kablolamasının uçtan uca testleri. TEST_DATABASE_URL yoksa atlanır;
// LLM sahte (synthVotesChat) — canlı LLM/DB'ye dokunmaz. Oylama/karar
// mantığının kendisi DB'siz gate_thirdparty_test.go'da sınanır.

// synthVotesChat: coherence/dedup/özgünlük/veri-erişimi normal davranır
// (pass); üçüncü-taraf v3 merceği tpVerdicts dizisini SIRAYLA döner (liste
// biterse "pass"); v1 üçüncü-taraf prompt'u v3 seçiliyken ÇAĞRILIRSA v1Calls
// artar (test bunu hata sayar).
type synthVotesChat struct {
	response   string
	tpVerdicts []string

	tpCalls, v1Calls, dataCalls, distinctCalls int
}

func (f *synthVotesChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return f.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (f *synthVotesChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	switch system {
	case coherenceSystem:
		return `{"indices":[0,1,2]}`, nil
	case dupJudgeSystem:
		return `{"same": false}`, nil
	case lensDistinctivenessSystem:
		f.distinctCalls++
		return `{"verdict":"pass","criterion":"none","reason":"ok"}`, nil
	case lensDataAccessSystem:
		f.dataCalls++
		return `{"verdict":"pass","reason":"ok"}`, nil
	case lensThirdPartySystem:
		f.v1Calls++
		return `{"verdict":"pass","reason":"v1"}`, nil
	case lensThirdPartySystemV3:
		idx := f.tpCalls
		f.tpCalls++
		v := "pass"
		if idx < len(f.tpVerdicts) {
			v = f.tpVerdicts[idx]
		}
		return fmt.Sprintf(`{"verdict":%q,"reason":"v3-oy-%d"}`, v, idx+1), nil
	}
	return f.response, nil
}

func synthVotesSetup(t *testing.T, title, platform, tag string) (*store.Store, context.Context, *config.Config, func()) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()
	st, err := store.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(st.Close)

	cleanup := func() {
		st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
		st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = $1", platform)
		st.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = $1", tag)
		st.Pool.Exec(ctx, "DELETE FROM eliminations WHERE subject = $1", title)
	}
	cleanup()
	t.Cleanup(cleanup)

	setupSynthTheme(t, ctx, st, platform, tag, false)
	cfg := &config.Config{
		MinThemeEvidence: 3, LLMSleepMS: 1, OutputLang: "tr",
		ThirdPartyPrompt: "v3", ThirdPartyVotes: 3,
	}
	return st, ctx, cfg, cleanup
}

func synthVotesCard(title, tag string) string {
	return fmt.Sprintf(`{"title":%q,"problem_statement":"sorun",
		"proposed_solution":"çözüm","target_user":"kullanıcı","example_quotes":["quote one"],
		"urgency_score":4,"monetization_signal":2,"known_competitors_ai_guess":"","domain_tags":[%q]}`, title, tag)
}

// TestSynthesizeIdeasThirdPartyVotesUnanimousBlocks: v3 + 3 oy, üçüncü-taraf
// 3/3 fail -> kart YAZILMAZ, veri-erişimi/özgünlük HİÇ çağrılmaz (organik:
// ilk fail'de dur), eliminations'a blocking_lens + 3 oy kaydı (prompt_version
// v3, "oy k/3: " önekli) düşer, tema damgalanır; v1 prompt'u hiç çağrılmaz.
func TestSynthesizeIdeasThirdPartyVotesUnanimousBlocks(t *testing.T) {
	title, platform, tag := "Test Ucuncu Taraf Oy Blok Fikri", "test-syn-tp-votes-block", "test-syn-tp-votes-block-tag"
	st, ctx, cfg, _ := synthVotesSetup(t, title, platform, tag)
	since := time.Now().Add(-time.Minute)

	chat := &synthVotesChat{response: synthVotesCard(title, tag), tpVerdicts: []string{"fail", "fail", "fail"}}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 0 {
		t.Fatalf("3/3 fail kartı bloklamalı, n=0 beklenirdi, geldi %d", n)
	}
	if chat.tpCalls != 3 || chat.v1Calls != 0 || chat.dataCalls != 0 || chat.distinctCalls != 0 {
		t.Errorf("üçüncü-taraf v3 3 çağrı, v1/veri-erişimi/özgünlük 0 çağrı beklenirdi: tp=%d v1=%d data=%d distinct=%d",
			chat.tpCalls, chat.v1Calls, chat.dataCalls, chat.distinctCalls)
	}

	var ideaCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM ideas WHERE title = $1", title).Scan(&ideaCount); err != nil {
		t.Fatal(err)
	}
	if ideaCount != 0 {
		t.Error("bloklanan kart DB'ye yazılmamalı")
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	var found *store.Elimination
	for i := range elims {
		if elims[i].Stage == "blocking_lens" && elims[i].Subject == title {
			found = &elims[i]
		}
	}
	if found == nil {
		t.Fatal("blok eliminations'a stage=blocking_lens kaydı düşürmeli")
	}
	if found.Check == nil || *found.Check != lensThirdPartyName {
		t.Errorf("eliminations.check=%q beklenirdi, geldi: %v", lensThirdPartyName, found.Check)
	}
	if len(found.Verdicts) != 3 {
		t.Fatalf("eliminations.verdicts 3 oy kaydı beklenirdi, geldi %d: %+v", len(found.Verdicts), found.Verdicts)
	}
	for i, v := range found.Verdicts {
		wantReason := fmt.Sprintf("oy %d/3: v3-oy-%d", i+1, i+1)
		if v.Verdict != "fail" || v.PromptVersion != "v3" || v.Subject != "card" || v.Reason != wantReason {
			t.Errorf("kayıt %d (fail/v3/card/%q) beklenirdi, geldi: %+v", i, wantReason, v)
		}
	}

	var incoherentAt *time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT incoherent_at FROM themes WHERE theme_name = $1", tag).Scan(&incoherentAt); err != nil {
		t.Fatal(err)
	}
	if incoherentAt == nil {
		t.Error("bloklanan tema MarkThemeIncoherent ile damgalanmalı")
	}
}

// TestSynthesizeIdeasThirdPartyVotesDisputedWritesCard: v3 + 3 oy, oylar
// ayrışır (fail, pass) -> mercek "unsure/tartışmalı", kart YAZILIR (bloklamaz),
// veri-erişimi+özgünlük çağrılır, oy kayıtları kartın lens_verdicts'ine
// girer (2 oy + veri-erişimi + özgünlük), eliminations'a kayıt DÜŞMEZ.
func TestSynthesizeIdeasThirdPartyVotesDisputedWritesCard(t *testing.T) {
	title, platform, tag := "Test Ucuncu Taraf Oy Tartismali Fikri", "test-syn-tp-votes-disp", "test-syn-tp-votes-disp-tag"
	st, ctx, cfg, _ := synthVotesSetup(t, title, platform, tag)
	since := time.Now().Add(-time.Minute)

	chat := &synthVotesChat{response: synthVotesCard(title, tag), tpVerdicts: []string{"fail", "pass", "fail"}}

	n, err := SynthesizeIdeas(ctx, cfg, st, chat)
	if err != nil {
		t.Fatalf("SynthesizeIdeas: %v", err)
	}
	if n != 1 {
		t.Fatalf("ayrışma kartı bloklamamalı, n=1 beklenirdi, geldi %d", n)
	}
	if chat.tpCalls != 2 || chat.v1Calls != 0 || chat.dataCalls != 1 || chat.distinctCalls != 1 {
		t.Errorf("üçüncü-taraf 2 çağrı (erken çıkış), veri-erişimi 1, özgünlük 1 beklenirdi: tp=%d v1=%d data=%d distinct=%d",
			chat.tpCalls, chat.v1Calls, chat.dataCalls, chat.distinctCalls)
	}

	lv := mustLensVerdicts(t, ctx, st, title)
	if len(lv) != 4 {
		t.Fatalf("lens_verdicts 4 kayıt beklenirdi (2 oy + veri-erişimi + özgünlük), geldi %d: %+v", len(lv), lv)
	}
	if lv[0].Lens != lensThirdPartyName || lv[0].PromptVersion != "v3" || !strings.HasPrefix(lv[0].Reason, "oy 1/3: ") || lv[0].Verdict != "fail" {
		t.Errorf("lv[0] üçüncü-taraf v3 oy 1/3 fail olmalı: %+v", lv[0])
	}
	if lv[1].Verdict != "pass" || !strings.HasPrefix(lv[1].Reason, "oy 2/3: ") {
		t.Errorf("lv[1] oy 2/3 pass olmalı: %+v", lv[1])
	}
	if lv[2].Lens != "veri-erişimi" || lv[3].Lens != "özgünlük" {
		t.Errorf("lv[2]/lv[3] veri-erişimi/özgünlük olmalı: %+v %+v", lv[2], lv[3])
	}

	elims, err := st.EliminationsSince(ctx, since)
	if err != nil {
		t.Fatalf("EliminationsSince: %v", err)
	}
	for _, e := range elims {
		if e.Subject == title {
			t.Errorf("tartışmalı kart için eliminations kaydı OLMAMALI: %+v", e)
		}
	}
}
