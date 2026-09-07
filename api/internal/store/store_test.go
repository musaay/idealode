package store

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Gerçek DB isteyen testler TEST_DATABASE_URL ile koşulur; yoksa atlanır.
// Örn: TEST_DATABASE_URL=postgres://postgres@127.0.0.1:54329/idealode_test go test ./internal/store
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	s, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestInsertRawPostsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test", SourceRef: "t-1", Community: "c", Title: "a"},
		{Platform: "test", SourceRef: "t-2", Community: "c", Title: "b"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test'")
	})

	n1, err := s.InsertRawPosts(ctx, posts)
	if err != nil {
		t.Fatalf("ilk insert: %v", err)
	}
	n2, err := s.InsertRawPosts(ctx, posts)
	if err != nil {
		t.Fatalf("ikinci insert: %v", err)
	}
	if n1 != 2 || n2 != 0 {
		t.Errorf("idempotency: ilk=%d (beklenen 2), ikinci=%d (beklenen 0)", n1, n2)
	}
}

// TestUnanalyzedPostsExcludesRadarSeed, ProcessSeeds'in işaret satırlarının
// (#56) analiz kuyruğuna hiç girmediğini doğrular.
func TestUnanalyzedPostsExcludesRadarSeed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test-radar-seed", SourceRef: "trs-1", Community: "c", Title: "normal post"},
		{Platform: "radar_seed", SourceRef: "trs-2", Community: "radar", Title: "tohum işaret"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ('test-radar-seed', 'radar_seed') AND source_ref LIKE 'trs-%'")
	})
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out, err := s.UnanalyzedPosts(ctx, 1000)
	if err != nil {
		t.Fatalf("UnanalyzedPosts: %v", err)
	}
	for _, p := range out {
		if p.Platform == "radar_seed" {
			t.Fatalf("radar_seed post analiz kuyruğuna girmemeli: %+v", p)
		}
	}
	found := false
	for _, p := range out {
		if p.SourceRef == "trs-1" {
			found = true
		}
	}
	if !found {
		t.Error("normal post analiz kuyruğunda görünmeli")
	}
}

// TestUnanalyzedPostsExcludesGitHubTrending, github_trending satırlarının
// (#50 B parçası) LLM sinyal sınıflandırmasına değil doğrudan füzyona
// girdiğini — dolayısıyla analiz kuyruğuna hiç girmediğini doğrular.
func TestUnanalyzedPostsExcludesGitHubTrending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	posts := []RawPost{
		{Platform: "test-gh-trending", SourceRef: "tgt-1", Community: "c", Title: "normal post"},
		{Platform: "github_trending", SourceRef: "tgt-2", Community: "daily", Title: "owner/repo"},
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform IN ('test-gh-trending', 'github_trending') AND source_ref LIKE 'tgt-%'")
	})
	if _, err := s.InsertRawPosts(ctx, posts); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out, err := s.UnanalyzedPosts(ctx, 1000)
	if err != nil {
		t.Fatalf("UnanalyzedPosts: %v", err)
	}
	for _, p := range out {
		if p.Platform == "github_trending" {
			t.Fatalf("github_trending post analiz kuyruğuna girmemeli: %+v", p)
		}
	}
	found := false
	for _, p := range out {
		if p.SourceRef == "tgt-1" {
			found = true
		}
	}
	if !found {
		t.Error("normal post analiz kuyruğunda görünmeli")
	}
}

// TestArchivedIdeaHidden, arşivlenmiş (archived_at dolu) kartın galeri
// listesinde görünmediğini ve GetIdea ile ErrNotFound döndüğünü doğrular;
// arşivsiz kart etkilenmemeli (#74).
func TestArchivedIdeaHidden(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	active, err := s.InsertIdea(ctx, Idea{
		Title:            "test-archive-active",
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("active insert: %v", err)
	}
	archived, err := s.InsertIdea(ctx, Idea{
		Title:            "test-archive-archived",
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("archived insert: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", active, archived)
	})

	// InsertIdea artık published_at YAZMAZ (#102 moderasyon kuyruğu) — bu
	// test archived_at davranışını izole ediyor, ikisini de PO onaylamış
	// (yayında) varsayalım ki arşivsiz kart published_at yüzünden değil
	// gerçekten arşivsiz olduğu için görünsün.
	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id IN ($1, $2)", active, archived); err != nil {
		t.Fatalf("publish update: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET archived_at = now() WHERE id = $1", archived); err != nil {
		t.Fatalf("archive update: %v", err)
	}

	// Liste: arşivli kart yok, arşivsiz kart var.
	out, err := s.ListIdeasFiltered(ctx, IdeaFilter{Limit: 200})
	if err != nil {
		t.Fatalf("ListIdeasFiltered: %v", err)
	}
	var sawActive, sawArchived bool
	for _, i := range out {
		if i.ID == active {
			sawActive = true
		}
		if i.ID == archived {
			sawArchived = true
		}
	}
	if !sawActive {
		t.Error("arşivsiz kart listede görünmeli")
	}
	if sawArchived {
		t.Error("arşivli kart listede görünmemeli")
	}

	// Detay: arşivsiz 200, arşivli ErrNotFound.
	if _, err := s.GetIdea(ctx, active, ""); err != nil {
		t.Errorf("GetIdea(arşivsiz): beklenmeyen hata: %v", err)
	}
	if _, err := s.GetIdea(ctx, archived, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIdea(arşivli): ErrNotFound bekleniyordu, alınan: %v", err)
	}
}

// TestPendingIdeaHiddenUntilPublished, #102 moderasyon kuyruğunu doğrular:
// InsertIdea'nın bıraktığı published_at=NULL kart galeride/detayda
// görünmez; PO onayını (published_at=now()) simüle eden UPDATE'ten SONRA
// görünür.
func TestPendingIdeaHiddenUntilPublished(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title:            "test-pending-idea",
		ProblemStatement: "p", ProposedSolution: "s", TargetUser: "u",
		SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id)
	})

	// InsertIdea published_at YAZMAZ -> beklemede -> ne galeride ne detayda.
	out, err := s.ListIdeasFiltered(ctx, IdeaFilter{Limit: 200})
	if err != nil {
		t.Fatalf("ListIdeasFiltered: %v", err)
	}
	for _, i := range out {
		if i.ID == id {
			t.Error("beklemedeki (published_at NULL) kart galeride görünmemeli")
		}
	}
	if _, err := s.GetIdea(ctx, id, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIdea(beklemede): ErrNotFound bekleniyordu, alınan: %v", err)
	}

	// PO onayı simülasyonu.
	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("publish update: %v", err)
	}

	out, err = s.ListIdeasFiltered(ctx, IdeaFilter{Limit: 200})
	if err != nil {
		t.Fatalf("ListIdeasFiltered (yayın sonrası): %v", err)
	}
	var seen bool
	for _, i := range out {
		if i.ID == id {
			seen = true
		}
	}
	if !seen {
		t.Error("published_at set edildikten sonra kart galeride görünmeli")
	}
	if _, err := s.GetIdea(ctx, id, ""); err != nil {
		t.Errorf("GetIdea(yayında): beklenmeyen hata: %v", err)
	}
}

// TestPendingIdeasQuery, PendingIdeas'ın yalnız beklemedeki (published_at
// NULL, archived_at NULL) kartları döndüğünü doğrular — `idealode run`
// özet logu (#102) bu sorguyla beslenir.
func TestPendingIdeasQuery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	pendingID, err := s.InsertIdea(ctx, Idea{
		Title: "test-pendinglist-pending", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("insert pending: %v", err)
	}
	publishedID, err := s.InsertIdea(ctx, Idea{
		Title: "test-pendinglist-published", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point",
	})
	if err != nil {
		t.Fatalf("insert published: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id IN ($1, $2)", pendingID, publishedID)
	})
	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id = $1", publishedID); err != nil {
		t.Fatalf("publish update: %v", err)
	}

	pending, err := s.PendingIdeas(ctx)
	if err != nil {
		t.Fatalf("PendingIdeas: %v", err)
	}
	var sawPending, sawPublished bool
	for _, p := range pending {
		if p.ID == pendingID {
			sawPending = true
		}
		if p.ID == publishedID {
			sawPublished = true
		}
	}
	if !sawPending {
		t.Error("beklemedeki kart PendingIdeas'ta olmalı")
	}
	if sawPublished {
		t.Error("yayındaki kart PendingIdeas'ta olmamalı")
	}
}

// TestInsertBlendedIdeaPublishedImmediately, blend kartının (#102)
// published_at=now() ile yazıldığını doğrular — GetIdea artık
// published_at IS NOT NULL aradığından, bu olmasaydı kart oluşur oluşmaz
// kendi sahibine bile görünmezdi.
func TestInsertBlendedIdeaPublishedImmediately(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sid := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	parentID, err := s.InsertIdea(ctx, Idea{
		Title: "test-blend-parent", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"},
	})
	if err != nil {
		t.Fatalf("parent insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", parentID) })

	// InsertBlendedIdea parent'ı yalnız Go struct'ı üzerinden okur, DB'den
	// yeniden çekmez — parent'ın kendisinin yayında olması (published_at)
	// gerekmez, elle kuruyoruz (GetIdea'ya gerek yok).
	parent := &Idea{ID: parentID, Title: "test-blend-parent", ProblemStatement: "p",
		ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"}}

	draft := BlendDraft{
		Title: "test-blend-child", ProblemStatement: "p2", ProposedSolution: "s2",
		TargetUser: "u2", DomainTags: []string{"y"}, UrgencyScore: 3, MonetizationSignal: 2,
	}
	blended, err := s.InsertBlendedIdea(ctx, parent, draft, sid)
	if err != nil {
		t.Fatalf("InsertBlendedIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", blended.ID) })

	// InsertBlendedIdea kendi içinde GetIdea çağırıp döner — hata dönmediyse
	// (yukarıda zaten kontrol edildi) published_at zaten dolu demektir; yine
	// de doğrudan kolonu da doğrulayalım.
	var publishedAt *string
	if err := s.Pool.QueryRow(ctx, "SELECT published_at::text FROM ideas WHERE id = $1", blended.ID).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt == nil {
		t.Error("blend kartının published_at'i NULL olmamalı (owner'a hemen görünmeli)")
	}

	// Sahibi (aynı sid) GetIdea ile görebilmeli.
	if _, err := s.GetIdea(ctx, blended.ID, sid); err != nil {
		t.Errorf("GetIdea(blend, sahibi): beklenmeyen hata: %v", err)
	}
}

func TestConnectSearchPath(t *testing.T) {
	s := testStore(t)

	var path string
	if err := s.Pool.QueryRow(context.Background(), "SHOW search_path").Scan(&path); err != nil {
		t.Fatalf("SHOW search_path: %v", err)
	}
	if path != "idealode, public" {
		t.Errorf("search_path beklenen değil: %q", path)
	}

	// search_path sayesinde şema öneki olmadan erişilebilmeli
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sources").Scan(&n); err != nil {
		t.Fatalf("sources sorgusu (search_path üzerinden): %v", err)
	}
	if n == 0 {
		t.Errorf("seed sonrası sources boş olmamalı")
	}
}
