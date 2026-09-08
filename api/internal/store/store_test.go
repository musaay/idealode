package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
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

// ------------------------------------------------------------ slug (#110)

// TestInsertIdeaGeneratesSlug, InsertIdea'nın her kart için taban+rastgele
// ek biçiminde, benzersiz bir slug yazdığını doğrular.
func TestInsertIdeaGeneratesSlug(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "Test Slug Üretimi Öğrenci Bütçesi", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	var slug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM ideas WHERE id = $1", id).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	wantBase := "test-slug-uretimi-ogrenci-butcesi"
	if !strings.HasPrefix(slug, wantBase+"-") {
		t.Errorf("slug = %q, %q- ile başlaması bekleniyordu", slug, wantBase)
	}
	if len(slug) != len(wantBase)+1+slugSuffixLen {
		t.Errorf("slug uzunluğu = %d (%q), beklenen taban+tire+%d karakter", len(slug), slug, slugSuffixLen)
	}
}

// TestInsertIdeaSlugConflictRetries, ilk denemede slug UNIQUE kısıtına
// çarpınca (isSlugConflict) InsertIdea'nın yeni bir rastgele ekle yeniden
// denediğini ve sonunda benzersiz bir slug ile başarılı olduğunu doğrular
// (#110 spec: "çakışmada UNIQUE ihlalinde 1 kez tekrar"). Gerçek çakışma
// crypto/rand ile pratikte olmayacağından, slugSuffixFn test süresince
// deterministik bir üretici ile değiştirilir.
func TestInsertIdeaSlugConflictRetries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	title := "Test Slug Çakışma Yeniden Deneme"

	firstID, err := s.InsertIdea(ctx, Idea{
		Title: title, ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 1,
	})
	if err != nil {
		t.Fatalf("ilk insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", firstID) })

	var firstSlug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM ideas WHERE id = $1", firstID).Scan(&firstSlug); err != nil {
		t.Fatal(err)
	}
	if len(firstSlug) < slugSuffixLen+1 {
		t.Fatalf("beklenmeyen slug biçimi: %q", firstSlug)
	}
	base := firstSlug[:len(firstSlug)-slugSuffixLen-1] // "-xxxx" öncesi
	suffix1 := firstSlug[len(firstSlug)-slugSuffixLen:]

	// Aynı başlıkla ikinci kart: taban aynı olacağından, ek de firstSlug'la
	// aynıysa (suffix1) UNIQUE ihlali garanti; ikinci denemede farklı bir
	// ek ("z9z9") ile başarılı olmalı.
	calls := 0
	orig := slugSuffixFn
	t.Cleanup(func() { slugSuffixFn = orig })
	slugSuffixFn = func() string {
		calls++
		if calls == 1 {
			return suffix1
		}
		return "z9z9"
	}

	secondID, err := s.InsertIdea(ctx, Idea{
		Title: title, ProblemStatement: "p2", ProposedSolution: "s2",
		TargetUser: "u2", SourceType: "pain_point", UrgencyScore: 1,
	})
	if err != nil {
		t.Fatalf("çakışma sonrası yeniden deneme başarısız olmamalı: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", secondID) })

	if calls < 2 {
		t.Errorf("çakışmada yeniden deneme tetiklenmedi, çağrı sayısı = %d", calls)
	}

	var secondSlug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM ideas WHERE id = $1", secondID).Scan(&secondSlug); err != nil {
		t.Fatal(err)
	}
	wantSecond := base + "-z9z9"
	if secondSlug != wantSecond {
		t.Errorf("ikinci kartın slug'ı = %q, beklenen %q", secondSlug, wantSecond)
	}
	if secondSlug == firstSlug {
		t.Error("çakışan slug tekrar kullanıldı — UNIQUE kısıtı atlanmış olurdu")
	}
}

// TestGetIdeaBySlug, GetIdea'nın uyguladığı görünürlük kurallarının
// (yayında + arşivsiz + ai_blended sahiplik) slug ile aramada da aynı
// şekilde çalıştığını doğrular.
func TestGetIdeaBySlug(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-getideabyslug-kart", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", UrgencyScore: 1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	var slug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM ideas WHERE id = $1", id).Scan(&slug); err != nil {
		t.Fatal(err)
	}

	// Beklemede (published_at NULL): ne id ne slug ile bulunur.
	if _, err := s.GetIdeaBySlug(ctx, slug, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIdeaBySlug(beklemede) = %v, ErrNotFound bekleniyordu", err)
	}

	if _, err := s.Pool.Exec(ctx, "UPDATE ideas SET published_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("publish update: %v", err)
	}

	got, err := s.GetIdeaBySlug(ctx, slug, "")
	if err != nil {
		t.Fatalf("GetIdeaBySlug(yayında): beklenmeyen hata: %v", err)
	}
	if got.ID != id || got.Slug != slug {
		t.Errorf("GetIdeaBySlug yanlış kart döndürdü: %+v", got)
	}

	// Sayısal görünen (id'nin kendisi) slug olarak aranmaz — kayıt yok.
	if _, err := s.GetIdeaBySlug(ctx, strconv.FormatInt(id, 10), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("sayısal id slug gibi arandı, ErrNotFound bekleniyordu: %v", err)
	}

	// Bilinmeyen slug -> ErrNotFound.
	if _, err := s.GetIdeaBySlug(ctx, "yok-boyle-bir-slug-0000", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetIdeaBySlug(bilinmeyen) = %v, ErrNotFound bekleniyordu", err)
	}
}

// TestInsertBlendedIdeaHasSlugAndParentSlug, blend kartının kendi slug'ını
// aldığını ve ideaSelect'in parent self-join'inin (#110) kaynak kartın
// slug'ını doğru döndürdüğünü doğrular.
func TestInsertBlendedIdeaHasSlugAndParentSlug(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sid := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	parentID, err := s.InsertIdea(ctx, Idea{
		Title: "test-blend-slug-parent", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"}, UrgencyScore: 1,
	})
	if err != nil {
		t.Fatalf("parent insert: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", parentID) })

	var parentSlug string
	if err := s.Pool.QueryRow(ctx, "SELECT slug FROM ideas WHERE id = $1", parentID).Scan(&parentSlug); err != nil {
		t.Fatal(err)
	}

	parent := &Idea{ID: parentID, Title: "test-blend-slug-parent", ProblemStatement: "p",
		ProposedSolution: "s", TargetUser: "u", SourceType: "pain_point", DomainTags: []string{"x"}}
	draft := BlendDraft{
		Title: "test-blend-slug-child", ProblemStatement: "p2", ProposedSolution: "s2",
		TargetUser: "u2", DomainTags: []string{"y"}, UrgencyScore: 3, MonetizationSignal: 2,
	}
	blended, err := s.InsertBlendedIdea(ctx, parent, draft, sid)
	if err != nil {
		t.Fatalf("InsertBlendedIdea: %v", err)
	}
	t.Cleanup(func() { s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", blended.ID) })

	if blended.Slug == "" {
		t.Error("blend kartının slug'ı boş olmamalı")
	}
	if blended.ParentSlug != parentSlug {
		t.Errorf("ParentSlug = %q, beklenen %q", blended.ParentSlug, parentSlug)
	}

	// GetIdeaBySlug ile de aynı ParentSlug gelmeli (self-join tekrar doğrulanır).
	again, err := s.GetIdeaBySlug(ctx, blended.Slug, sid)
	if err != nil {
		t.Fatalf("GetIdeaBySlug(blend): %v", err)
	}
	if again.ParentSlug != parentSlug {
		t.Errorf("GetIdeaBySlug sonrası ParentSlug = %q, beklenen %q", again.ParentSlug, parentSlug)
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
