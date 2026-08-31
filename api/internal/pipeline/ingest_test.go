package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/musaay/idealode/api/internal/connector"
	"github.com/musaay/idealode/api/internal/store"
)

// fakeConnector, canlı servise dokunmadan ingestSources'ı test etmek için
// FetchNew çağrılarını yakalayan sahte bir SourceConnector.
type fakeConnector struct {
	platform string
	fetch    func(ctx context.Context, src store.Source) ([]store.RawPost, string, error)
}

func (f *fakeConnector) Platform() string { return f.platform }

func (f *fakeConnector) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	return f.fetch(ctx, src)
}

// fakeInserter, InsertRawPosts/UpdateSourceLastSeen çağrılarını bellekte
// tutan sahte store — canlı DB kullanılmaz.
type fakeInserter struct {
	mu       sync.Mutex
	inserted []store.RawPost
	lastSeen map[int64]string
}

func newFakeInserter() *fakeInserter {
	return &fakeInserter{lastSeen: map[int64]string{}}
}

func (f *fakeInserter) InsertRawPosts(ctx context.Context, posts []store.RawPost) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, posts...)
	return len(posts), nil
}

func (f *fakeInserter) UpdateSourceLastSeen(ctx context.Context, sourceID int64, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSeen[sourceID] = ref
	return nil
}

func TestGroupSourcesByPlatform(t *testing.T) {
	sources := []store.Source{
		{ID: 1, Platform: "hackernews", Community: "hn1"},
		{ID: 2, Platform: "github", Community: "gh1"},
		{ID: 3, Platform: "hackernews", Community: "hn2"},
		{ID: 4, Platform: "github", Community: "gh2"},
		{ID: 5, Platform: "stackexchange", Community: "se1"},
	}

	platforms, groups := groupSourcesByPlatform(sources)

	wantPlatforms := []string{"hackernews", "github", "stackexchange"}
	if len(platforms) != len(wantPlatforms) {
		t.Fatalf("platform sayısı = %d, beklenen %d (%v)", len(platforms), len(wantPlatforms), platforms)
	}
	for i, p := range wantPlatforms {
		if platforms[i] != p {
			t.Errorf("platforms[%d] = %q, beklenen %q (ilk görülme sırası korunmalı)", i, platforms[i], p)
		}
	}

	if got := groups["hackernews"]; len(got) != 2 || got[0].Community != "hn1" || got[1].Community != "hn2" {
		t.Errorf("hackernews grubu sıralı değil: %+v", got)
	}
	if got := groups["github"]; len(got) != 2 || got[0].Community != "gh1" || got[1].Community != "gh2" {
		t.Errorf("github grubu sıralı değil: %+v", got)
	}
	if got := groups["stackexchange"]; len(got) != 1 || got[0].Community != "se1" {
		t.Errorf("stackexchange grubu hatalı: %+v", got)
	}
}

// TestIngestSourcesPlatformInternalOrderIsSerial, aynı platformun
// kaynaklarının -- goroutine zamanlamasından bağımsız olarak -- mevcut
// sırayla, tek goroutine'de (paralel değil) işlendiğini doğrular.
func TestIngestSourcesPlatformInternalOrderIsSerial(t *testing.T) {
	sources := []store.Source{
		{ID: 1, Platform: "a", Community: "a1"},
		{ID: 2, Platform: "b", Community: "b1"},
		{ID: 3, Platform: "a", Community: "a2"},
		{ID: 4, Platform: "b", Community: "b2"},
		{ID: 5, Platform: "a", Community: "a3"},
	}

	var mu sync.Mutex
	callOrder := map[string][]string{}
	inFlight := map[string]bool{}

	makeFetch := func(platform string) func(context.Context, store.Source) ([]store.RawPost, string, error) {
		return func(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
			mu.Lock()
			if inFlight[platform] {
				mu.Unlock()
				t.Errorf("platform içi paralel çağrı tespit edildi: %s", platform)
				return nil, "", nil
			}
			inFlight[platform] = true
			callOrder[platform] = append(callOrder[platform], src.Community)
			mu.Unlock()

			// Aynı platformdan bir sonraki çağrı gelirse üstteki guard
			// yakalasın diye kısa bir süre "içeride" kal.
			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			inFlight[platform] = false
			mu.Unlock()
			return nil, "", nil
		}
	}

	connectors := map[string]connector.SourceConnector{
		"a": &fakeConnector{platform: "a", fetch: makeFetch("a")},
		"b": &fakeConnector{platform: "b", fetch: makeFetch("b")},
	}

	inserter := newFakeInserter()
	if _, err := ingestSources(context.Background(), sources, connectors, inserter); err != nil {
		t.Fatalf("ingestSources hata döndü: %v", err)
	}

	wantA := []string{"a1", "a2", "a3"}
	if got := callOrder["a"]; !equalStrings(got, wantA) {
		t.Errorf("platform a sırası = %v, beklenen %v", got, wantA)
	}
	wantB := []string{"b1", "b2"}
	if got := callOrder["b"]; !equalStrings(got, wantB) {
		t.Errorf("platform b sırası = %v, beklenen %v", got, wantB)
	}
}

// TestIngestSourcesCrossPlatformParallel, farklı platformların birbirini
// beklemeden eşzamanlı işlendiğini kanıtlar: her platform bir bariyerde
// bekler, tüm platformlar bariyere ulaşmadan hiçbiri devam edemez. Kod
// sıralı çalışsaydı bu test zaman aşımına uğrardı.
func TestIngestSourcesCrossPlatformParallel(t *testing.T) {
	const platformCount = 4
	sources := make([]store.Source, 0, platformCount)
	connectors := map[string]connector.SourceConnector{}

	var wg sync.WaitGroup
	wg.Add(platformCount)
	barrierDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(barrierDone)
	}()

	for i := 0; i < platformCount; i++ {
		platform := string(rune('A' + i))
		sources = append(sources, store.Source{ID: int64(i + 1), Platform: platform, Community: platform})
		connectors[platform] = &fakeConnector{
			platform: platform,
			fetch: func(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
				wg.Done()
				select {
				case <-barrierDone:
				case <-time.After(2 * time.Second):
					t.Errorf("%s: bariyer zaman aşımına uğradı — platformlar paralel çalışmıyor olabilir", src.Platform)
				}
				return nil, "", nil
			},
		}
	}

	inserter := newFakeInserter()
	done := make(chan error, 1)
	go func() {
		_, err := ingestSources(context.Background(), sources, connectors, inserter)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ingestSources hata döndü: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ingestSources zaman aşımına uğradı — platformlar arası paralellik yok")
	}
}

// TestIngestSourcesErrorIsolationAndCounter, kaynak başına hata
// izolasyonunu, last_seen_ref semantiğini ve yarışsız toplam sayacı
// doğrular.
func TestIngestSourcesErrorIsolationAndCounter(t *testing.T) {
	sources := []store.Source{
		{ID: 1, Platform: "ok", Community: "ok1"},
		{ID: 2, Platform: "fail", Community: "fail1"},
		{ID: 3, Platform: "ok", Community: "ok2"},
		{ID: 4, Platform: "noconn", Community: "nc1"},
	}

	connectors := map[string]connector.SourceConnector{
		"ok": &fakeConnector{platform: "ok", fetch: func(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
			return []store.RawPost{{Platform: "ok", SourceRef: src.Community}}, "ref-" + src.Community, nil
		}},
		"fail": &fakeConnector{platform: "fail", fetch: func(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
			return nil, "", context.DeadlineExceeded
		}},
		// "noconn" platformu connectors map'inde yok -> "connector yok" yolu.
	}

	inserter := newFakeInserter()
	total, err := ingestSources(context.Background(), sources, connectors, inserter)
	if err != nil {
		t.Fatalf("ingestSources hata döndü: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, beklenen 2 (yalnız ok platformu 2 kayıt ekler)", total)
	}
	if len(inserter.inserted) != 2 {
		t.Errorf("inserted kayıt sayısı = %d, beklenen 2", len(inserter.inserted))
	}
	if inserter.lastSeen[1] != "ref-ok1" || inserter.lastSeen[3] != "ref-ok2" {
		t.Errorf("last_seen_ref güncellenmemiş: %+v", inserter.lastSeen)
	}
	if _, ok := inserter.lastSeen[2]; ok {
		t.Errorf("hata veren kaynağın last_seen_ref'i güncellenmemeli")
	}
}

func equalStrings(a, b []string) bool {
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
