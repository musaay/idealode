package pipeline

import (
	"context"
	"os"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

// scrubTestStore, gerçek DB isteyen scrub-quotes testleri için ortak kurulum
// (TEST_DATABASE_URL yoksa atlanır — retheme_test.go'daki desenle aynı).
func scrubTestStore(t *testing.T) (*store.Store, context.Context) {
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
	return st, ctx
}

// insertScrubTestIdea, scrub-quotes testleri için doğrudan SQL ile (store
// yazım sınırındaki profanity guard'ı BYPASS ederek) küfürlü satır içeren
// bir kart yazar — amaç, guard ÖNCESİ yazılmış "eski kart" senaryosunu
// simüle etmek (#100 scrub-quotes'un asıl hedef kitlesi).
func insertScrubTestIdea(t *testing.T, ctx context.Context, st *store.Store, title string, quotes, evidence []string) int64 {
	t.Helper()
	// local_evidence NOT NULL DEFAULT '{}' — nil burada guard'sız ham SQL'e
	// gittiğinden testte de nil-slice tuzağına düşmemek için elle indirgenir.
	if quotes == nil {
		quotes = []string{}
	}
	if evidence == nil {
		evidence = []string{}
	}
	var id int64
	err := st.Pool.QueryRow(ctx, `
		INSERT INTO ideas
			(title, slug, problem_statement, proposed_solution, target_user, evidence_count,
			 example_quotes, source_type, domain_tags, local_evidence, published_at)
		VALUES ($1, $1, 'p', 's', 'u', 1, $2, 'pain_point', '{}', $3, now())
		RETURNING id`, title, quotes, evidence).Scan(&id)
	if err != nil {
		t.Fatalf("insertScrubTestIdea: %v", err)
	}
	return id
}

func readScrubTestIdea(t *testing.T, ctx context.Context, st *store.Store, id int64) (quotes, evidence []string) {
	t.Helper()
	if err := st.Pool.QueryRow(ctx,
		`SELECT example_quotes, local_evidence FROM ideas WHERE id = $1`, id).Scan(&quotes, &evidence); err != nil {
		t.Fatalf("readScrubTestIdea: %v", err)
	}
	return quotes, evidence
}

// TestScrubQuotesDryRunWritesNothing, --dry-run'ın example_quotes/
// local_evidence'ı HİÇ değiştirmediğini doğrular (#100 kabul kriteri).
func TestScrubQuotesDryRunWritesNothing(t *testing.T) {
	st, ctx := scrubTestStore(t)

	id := insertScrubTestIdea(t, ctx, st, "test-scrub-dryrun",
		[]string{"clean quote", "this app is fucking broken"},
		[]string{"siktir git bu uygulamadan"})
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	result, err := ScrubQuotes(ctx, st, true)
	if err != nil {
		t.Fatalf("ScrubQuotes dry-run: %v", err)
	}
	if result.CardsAffected < 1 {
		t.Errorf("dry-run: CardsAffected = %d, want >= 1", result.CardsAffected)
	}

	quotes, evidence := readScrubTestIdea(t, ctx, st, id)
	if len(quotes) != 2 {
		t.Errorf("dry-run sonrası example_quotes değişmiş: %v (len %d), want len 2", quotes, len(quotes))
	}
	if len(evidence) != 1 {
		t.Errorf("dry-run sonrası local_evidence değişmiş: %v (len %d), want len 1", evidence, len(evidence))
	}
}

// TestScrubQuotesRealModeCleansAndIsIdempotent, gerçek modun küfürlü
// satırları attığını ve ikinci koşunun no-op olduğunu (idempotent, #100
// kabul kriteri) doğrular.
func TestScrubQuotesRealModeCleansAndIsIdempotent(t *testing.T) {
	st, ctx := scrubTestStore(t)

	id := insertScrubTestIdea(t, ctx, st, "test-scrub-real",
		[]string{"clean quote", "this app is fucking broken"},
		[]string{"siktir git bu uygulamadan", "temiz bir kanıt satırı"})
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	result, err := ScrubQuotes(ctx, st, false)
	if err != nil {
		t.Fatalf("ScrubQuotes: %v", err)
	}
	if result.CardsAffected < 1 {
		t.Fatalf("CardsAffected = %d, want >= 1", result.CardsAffected)
	}

	quotes, evidence := readScrubTestIdea(t, ctx, st, id)
	if len(quotes) != 1 || quotes[0] != "clean quote" {
		t.Errorf("example_quotes = %v, want [\"clean quote\"]", quotes)
	}
	if len(evidence) != 1 || evidence[0] != "temiz bir kanıt satırı" {
		t.Errorf("local_evidence = %v, want [\"temiz bir kanıt satırı\"]", evidence)
	}
	if quotes == nil || evidence == nil {
		t.Error("nil-slice tuzağı: quotes/evidence NULL olmamalı, boş dilim olmalı")
	}

	// İkinci koşu: kart artık temiz, hiçbir satır düşmemeli (idempotent).
	result2, err := ScrubQuotes(ctx, st, false)
	if err != nil {
		t.Fatalf("ScrubQuotes (ikinci koşu): %v", err)
	}
	quotes2, evidence2 := readScrubTestIdea(t, ctx, st, id)
	if len(quotes2) != 1 || len(evidence2) != 1 {
		t.Errorf("ikinci koşu sonrası değişmiş: quotes=%v evidence=%v", quotes2, evidence2)
	}
	_ = result2
}

// TestScrubQuotesAllQuotesDroppedYieldsEmptySlice, tüm alıntılar
// düştüğünde kartın NULL değil boş dilimle yazıldığını doğrular (nil-slice
// guard, #100 kenar durumu).
func TestScrubQuotesAllQuotesDroppedYieldsEmptySlice(t *testing.T) {
	st, ctx := scrubTestStore(t)

	id := insertScrubTestIdea(t, ctx, st, "test-scrub-all-dropped",
		[]string{"this app is fucking broken", "such an idiot move"},
		nil)
	t.Cleanup(func() { st.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id) })

	if _, err := ScrubQuotes(ctx, st, false); err != nil {
		t.Fatalf("ScrubQuotes: %v", err)
	}

	quotes, evidence := readScrubTestIdea(t, ctx, st, id)
	if quotes == nil {
		t.Error("example_quotes NULL döndü, want boş (non-nil) dilim")
	}
	if len(quotes) != 0 {
		t.Errorf("example_quotes = %v, want boş dilim", quotes)
	}
	if evidence == nil {
		t.Error("local_evidence NULL döndü, want boş (non-nil) dilim")
	}
}
