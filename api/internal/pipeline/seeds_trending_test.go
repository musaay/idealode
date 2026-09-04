package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/connector"
	"github.com/musaay/idealode/api/internal/store"
)

// --- evaluateTrendingGate birim testleri (#89 kapı madde 6, 1, 2, 3 — fake
// store + fake GitHub meta ile, canlı DB/ağ YOK) ---

// fakeGateStore, trendingGateStore'u sabit değerlerle taklit eder.
type fakeGateStore struct {
	weeklyCount    int
	weeklyErr      error
	persistDays    int
	persistDelta   int
	persistErr     error
	gotCountKind   string
	gotPersistRepo string
}

func (f *fakeGateStore) CountIdeasSince(ctx context.Context, sourceType string, sinceDays int) (int, error) {
	f.gotCountKind = sourceType
	return f.weeklyCount, f.weeklyErr
}

func (f *fakeGateStore) TrendingPersistenceDays(ctx context.Context, repo string, sinceDays int) (int, int, error) {
	f.gotPersistRepo = repo
	return f.persistDays, f.persistDelta, f.persistErr
}

func fakeMeta(meta connector.RepoMeta, err error) trendingMetaFetcher {
	return func(ctx context.Context, owner, repo string) (connector.RepoMeta, error) {
		return meta, err
	}
}

func trendingTestSeed() radarSeed {
	return radarSeed{
		Name: "Test Repo", Summary: "özet", Evidence: "★1000, +50/hafta (GitHub trending)",
		SourceURL: "https://github.com/acme/widget", TRAngle: "TR açısı", Kind: "trending",
	}
}

func TestEvaluateTrendingGateWeeklyCapBlocksAndRetries(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 1}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(connector.RepoMeta{}, nil), trendingTestSeed())
	if got.action != trendingGateRetry {
		t.Fatalf("madde 6 (haftalık tavan): retry beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
	if st.gotCountKind != "momentum_derived" {
		t.Errorf("CountIdeasSince yanlış source_type ile çağrıldı: %q", st.gotCountKind)
	}
}

func TestEvaluateTrendingGatePersistenceTooLowRejects(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 2}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(connector.RepoMeta{}, nil), trendingTestSeed())
	if got.action != trendingGateReject {
		t.Fatalf("madde 1 (kalıcılık <3): reject beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
	if st.gotPersistRepo != "acme/widget" {
		t.Errorf("TrendingPersistenceDays yanlış repo ile çağrıldı: %q", st.gotPersistRepo)
	}
}

func TestEvaluateTrendingGateRepoNotFoundRejects(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 5}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(connector.RepoMeta{}, connector.ErrRepoNotFound), trendingTestSeed())
	if got.action != trendingGateReject {
		t.Fatalf("404: reject beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
}

func TestEvaluateTrendingGateRateLimitedRetries(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 5}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(connector.RepoMeta{}, connector.ErrGitHubRateLimited), trendingTestSeed())
	if got.action != trendingGateRetry {
		t.Fatalf("403/429: retry (imleç yazılmadan atla) beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
}

func TestEvaluateTrendingGateTooOldRejects(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 5}
	old := connector.RepoMeta{CreatedAt: time.Now().Add(-400 * 24 * time.Hour), StargazersCount: 1000, ForksCount: 100, OpenIssuesCount: 30}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(old, nil), trendingTestSeed())
	if got.action != trendingGateReject {
		t.Fatalf("madde 2 (yaş >12 ay): reject beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
}

func TestEvaluateTrendingGateLowUsageRejects(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 5}
	meta := connector.RepoMeta{CreatedAt: time.Now().Add(-30 * 24 * time.Hour), StargazersCount: 1000, ForksCount: 10, OpenIssuesCount: 5}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(meta, nil), trendingTestSeed())
	if got.action != trendingGateReject {
		t.Fatalf("madde 3 (gerçek kullanım yetersiz): reject beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
}

func TestEvaluateTrendingGatePassesOnOpenIssuesAlone(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 4, persistDelta: 77}
	meta := connector.RepoMeta{CreatedAt: time.Now().Add(-30 * 24 * time.Hour), StargazersCount: 1000, ForksCount: 10, OpenIssuesCount: 25}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(meta, nil), trendingTestSeed())
	if got.action != trendingGatePass {
		t.Fatalf("open_issues≥20 tek başına yetmeli: pass beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
	if got.repo != "acme/widget" || got.stars != 1000 || got.lastDelta != 77 || got.persistedDays != 4 {
		t.Errorf("yanlış gate sonucu: %+v", got)
	}
}

func TestEvaluateTrendingGatePassesOnForksRatioAlone(t *testing.T) {
	st := &fakeGateStore{weeklyCount: 0, persistDays: 4}
	meta := connector.RepoMeta{CreatedAt: time.Now().Add(-30 * 24 * time.Hour), StargazersCount: 1000, ForksCount: 60, OpenIssuesCount: 5}
	got := evaluateTrendingGate(context.Background(), st, fakeMeta(meta, nil), trendingTestSeed())
	if got.action != trendingGatePass {
		t.Fatalf("forks/stars≥0.05 tek başına yetmeli: pass beklenirdi, geldi: %v (%s)", got.action, got.reason)
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/widget":  "acme/widget",
		"https://github.com/acme/widget/": "acme/widget",
		"http://github.com/acme/widget":   "acme/widget",
		"https://example.com/not-github":  "",
		"https://github.com/only-owner":   "",
	}
	for in, want := range cases {
		got, ok := parseOwnerRepo(in)
		if want == "" {
			if ok {
				t.Errorf("parseOwnerRepo(%q): ok bekleniyordu false, geldi true (%q)", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("parseOwnerRepo(%q) = %q,%v; beklenen %q,true", in, got, ok, want)
		}
	}
}

func TestMomentumEvidenceLineFormat(t *testing.T) {
	got := momentumEvidenceLine(1234, 56, 5)
	want := "★1234 toplam, +56/gün (GitHub trending, son 14 günde 5 gün listede) — gelir kanıtı yok"
	if got != want {
		t.Errorf("evidence satırı beklenmiyor: %q != %q", got, want)
	}
}

// fetchTrendingRepoMeta paket başlatılırken connector.FetchRepoMeta'ya
// sabitlenir; testler bunu geçici olarak değiştirir (withFakeRepoMeta) ama
// varsayılan nil olmamalı.
func TestFetchTrendingRepoMetaVarNotNilByDefault(t *testing.T) {
	if fetchTrendingRepoMeta == nil {
		t.Fatal("fetchTrendingRepoMeta nil olamaz")
	}
}

// --- ProcessSeeds tam akış entegrasyonu (#89 AC2, AC3) — DB gerektirir,
// TEST_DATABASE_URL yoksa atlanır (bkz. seedTestStore). ---

// fakeMomentumChat: 4 mercek (ürünleştirilebilirlik + mevcut 3) + kart
// üretimi için sabit cevap döner.
type fakeMomentumChat struct {
	lensVerdict  string
	cardResponse string
}

func (f *fakeMomentumChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	if strings.Contains(system, `"momentum_derived"`) {
		return f.cardResponse, nil
	}
	return fmt.Sprintf(`{"verdict":%q,"reason":"test-reason"}`, f.lensVerdict), nil
}

// seedTrendingRawPosts, verilen repo için son `days` günün her birinde bir
// github_trending raw_posts satırı yazar (kalıcılık kapısını geçirmek için).
func seedTrendingRawPosts(t *testing.T, st *store.Store, repo string, days int, lastScore int) {
	t.Helper()
	ctx := context.Background()
	for d := 0; d < days; d++ {
		date := time.Now().UTC().AddDate(0, 0, -d).Format("2006-01-02")
		score := 10
		if d == 0 {
			score = lastScore
		}
		post := store.RawPost{
			Platform:  "github_trending",
			SourceRef: repo + ":" + date,
			Community: "daily",
			Title:     repo,
			Body:      "test",
			URL:       "https://github.com/" + repo,
			Score:     score,
			CreatedAt: time.Now().UTC().AddDate(0, 0, -d),
		}
		if _, err := st.InsertRawPosts(ctx, []store.RawPost{post}); err != nil {
			t.Fatalf("seedTrendingRawPosts: %v", err)
		}
	}
}

func cleanupTrendingRepo(t *testing.T, st *store.Store, repo, seedURL, title string) {
	t.Helper()
	ctx := context.Background()
	st.Pool.Exec(ctx, "DELETE FROM ideas WHERE title = $1", title)
	st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE source_ref = $1", seedURL)
	st.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'github_trending' AND split_part(source_ref, ':', 1) = $1", repo)
}

func withFakeRepoMeta(t *testing.T, meta connector.RepoMeta, err error) {
	t.Helper()
	prev := fetchTrendingRepoMeta
	fetchTrendingRepoMeta = fakeMeta(meta, err)
	t.Cleanup(func() { fetchTrendingRepoMeta = prev })
}

func TestProcessSeedsTrendingPersistenceTooLowRejectsNoCard(t *testing.T) {
	st := seedTestStore(t)
	repo := "acme/persist-low"
	seedURL := "https://github.com/" + repo
	title := "Ivme Kalicilik Az"
	cleanupTrendingRepo(t, st, repo, seedURL, title)
	t.Cleanup(func() { cleanupTrendingRepo(t, st, repo, seedURL, title) })

	// Yalnız 2 farklı gün (< 3 gerekli) -> elenir.
	seedTrendingRawPosts(t, st, repo, 2, 40)
	withFakeRepoMeta(t, connector.RepoMeta{
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour), StargazersCount: 1000, ForksCount: 100, OpenIssuesCount: 30,
	}, nil)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":%q,"summary":"özet","evidence":"★1000, +40/hafta (GitHub trending)","source_url":%q,"tr_angle":"t","kind":"trending"}`, title, seedURL)
	chat := &fakeMomentumChat{lensVerdict: "pass"}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	n, err := ProcessSeeds(context.Background(), cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 0 {
		t.Fatalf("0 idea beklenirdi (kalıcılık<3), geldi: %d", n)
	}

	var markCount int
	if err := st.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURL).Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 1 {
		t.Errorf("kalıcılık elemesinde imleç YAZILMALI (bir daha denenmesin), geldi: %d", markCount)
	}
}

func TestProcessSeedsTrendingPassCreatesMomentumCard(t *testing.T) {
	st := seedTestStore(t)
	repo := "acme/persist-ok"
	seedURL := "https://github.com/" + repo
	title := "Ivme Karti XYZ"
	cleanupTrendingRepo(t, st, repo, seedURL, title)
	t.Cleanup(func() { cleanupTrendingRepo(t, st, repo, seedURL, title) })

	// 3 farklı gün -> kalıcılık şartı geçer; son günün skoru 42.
	seedTrendingRawPosts(t, st, repo, 3, 42)
	withFakeRepoMeta(t, connector.RepoMeta{
		CreatedAt: time.Now().Add(-60 * 24 * time.Hour), StargazersCount: 800, ForksCount: 80, OpenIssuesCount: 30,
	}, nil)

	jsonl := fmt.Sprintf(`{"date":"2026-01-01","name":%q,"summary":"özet","evidence":"★800, +42/hafta (GitHub trending)","source_url":%q,"tr_angle":"t","kind":"trending"}`, title, seedURL)
	chat := &fakeMomentumChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":3,"monetization_signal":1,
			"known_competitors_ai_guess":"","domain_tags":["test-momentum-tag"]}`, title),
	}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	n, err := ProcessSeeds(context.Background(), cfg, st, chat, jsonl)
	if err != nil {
		t.Fatalf("ProcessSeeds: %v", err)
	}
	if n != 1 {
		t.Fatalf("1 idea beklenirdi, geldi: %d", n)
	}

	var sourceType string
	var monetization int
	var quotes []string
	if err := st.Pool.QueryRow(context.Background(),
		"SELECT source_type, monetization_signal, example_quotes FROM ideas WHERE title = $1", title).
		Scan(&sourceType, &monetization, &quotes); err != nil {
		t.Fatalf("idea yazılmamış: %v", err)
	}
	if sourceType != "momentum_derived" {
		t.Errorf("source_type=momentum_derived beklenirdi, geldi: %s", sourceType)
	}
	if monetization > 2 {
		t.Errorf("monetization_signal ≤2 beklenirdi (gelir kanıtı yok), geldi: %d", monetization)
	}
	if len(quotes) != 1 || !strings.Contains(quotes[0], "★800 toplam") || !strings.Contains(quotes[0], "gelir kanıtı yok") {
		t.Errorf("example_quotes koddan hesaplanmış ivme kanıtı olmalı, geldi: %v", quotes)
	}
}

func TestProcessSeedsTrendingWeeklyCapSkipsSecondSeedNoCursor(t *testing.T) {
	st := seedTestStore(t)
	repoA := "acme/cap-a"
	repoB := "acme/cap-b"
	seedURLA := "https://github.com/" + repoA
	seedURLB := "https://github.com/" + repoB
	titleA := "Ivme Tavan A"
	titleB := "Ivme Tavan B"
	cleanupTrendingRepo(t, st, repoA, seedURLA, titleA)
	cleanupTrendingRepo(t, st, repoB, seedURLB, titleB)
	t.Cleanup(func() {
		cleanupTrendingRepo(t, st, repoA, seedURLA, titleA)
		cleanupTrendingRepo(t, st, repoB, seedURLB, titleB)
	})

	seedTrendingRawPosts(t, st, repoA, 3, 10)
	seedTrendingRawPosts(t, st, repoB, 3, 10)
	withFakeRepoMeta(t, connector.RepoMeta{
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour), StargazersCount: 500, ForksCount: 50, OpenIssuesCount: 30,
	}, nil)

	chat := &fakeMomentumChat{
		lensVerdict: "pass",
		cardResponse: fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
			"target_user":"kullanıcı","urgency_score":3,"monetization_signal":1,
			"known_competitors_ai_guess":"","domain_tags":["test-momentum-tag"]}`, titleA),
	}
	cfg := &config.Config{OutputLang: "tr", LLMSleepMS: 1}

	// 1. tohum: kart üretilir.
	jsonlA := fmt.Sprintf(`{"date":"2026-01-01","name":%q,"summary":"özet","evidence":"e","source_url":%q,"tr_angle":"t","kind":"trending"}`, titleA, seedURLA)
	n1, err := ProcessSeeds(context.Background(), cfg, st, chat, jsonlA)
	if err != nil {
		t.Fatalf("ProcessSeeds (1. tohum): %v", err)
	}
	if n1 != 1 {
		t.Fatalf("1. tohumda 1 idea beklenirdi, geldi: %d", n1)
	}

	// 2. tohum (aynı koşu penceresi içinde, 7 gün dolmadan): tavan dolu ->
	// atlanır, imleç YAZILMAZ.
	chat.cardResponse = fmt.Sprintf(`{"title":%q,"problem_statement":"sorun","proposed_solution":"çözüm",
		"target_user":"kullanıcı","urgency_score":3,"monetization_signal":1,
		"known_competitors_ai_guess":"","domain_tags":["test-momentum-tag"]}`, titleB)
	jsonlB := fmt.Sprintf(`{"date":"2026-01-02","name":%q,"summary":"özet","evidence":"e","source_url":%q,"tr_angle":"t","kind":"trending"}`, titleB, seedURLB)
	n2, err := ProcessSeeds(context.Background(), cfg, st, chat, jsonlB)
	if err != nil {
		t.Fatalf("ProcessSeeds (2. tohum): %v", err)
	}
	if n2 != 0 {
		t.Fatalf("2. tohumda 0 idea beklenirdi (haftalık tavan dolu), geldi: %d", n2)
	}

	var markCount int
	if err := st.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM raw_posts WHERE platform = 'radar_seed' AND source_ref = $1", seedURLB).Scan(&markCount); err != nil {
		t.Fatal(err)
	}
	if markCount != 0 {
		t.Errorf("haftalık tavanda imleç YAZILMAMALI (sonraki haftaya yeniden denensin), geldi: %d", markCount)
	}

	var ideaBCount int
	if err := st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM ideas WHERE title = $1", titleB).Scan(&ideaBCount); err != nil {
		t.Fatal(err)
	}
	if ideaBCount != 0 {
		t.Errorf("2. tohumdan kart açılmamalı, geldi: %d", ideaBCount)
	}
}
