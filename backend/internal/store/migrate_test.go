package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestMigrateIdempotent, Migrate'in iki kez art arda hatasız çalıştığını
// doğrular (CLAUDE.md: "her migration idempotent olmalı, tüm dosyalar her
// koşuda yeniden çalıştırılır") — 014_distinctiveness.sql dahil tüm zincir.
// TEST_DATABASE_URL gerektirir (canlı prod'a DOKUNMAZ, ayrı test DB'si).
func TestMigrateIdempotent(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate (idempotent olmalı): %v", err)
	}

	// 014'ün eklediği kolonlar var mı ve nullable mı — InsertBlendedIdea
	// bu alanları YAZMAZ, kolonlar NOT NULL olursa o insert kırılır.
	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer s.Close()

	rows, err := s.Pool.Query(ctx, `
		SELECT column_name, is_nullable FROM information_schema.columns
		WHERE table_schema = 'idealode' AND table_name = 'ideas'
		  AND column_name IN ('distinctiveness_verdict', 'distinctiveness_criterion', 'distinctiveness_reason')`)
	if err != nil {
		t.Fatalf("information_schema sorgusu: %v", err)
	}
	defer rows.Close()

	found := map[string]string{}
	for rows.Next() {
		var col, nullable string
		if err := rows.Scan(&col, &nullable); err != nil {
			t.Fatal(err)
		}
		found[col] = nullable
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, col := range []string{"distinctiveness_verdict", "distinctiveness_criterion", "distinctiveness_reason"} {
		nullable, ok := found[col]
		if !ok {
			t.Errorf("kolon eksik: %s", col)
			continue
		}
		if nullable != "YES" {
			t.Errorf("%s NULL olabilmeli (blend kartları bu alanları hiç yazmaz), is_nullable=%s", col, nullable)
		}
	}
}

// TestMigratePublishedBackfillOnlyOnce, 015_published.sql'in backfill'inin
// yalnız kolon İLK eklendiğinde çalıştığını doğrular (reviewer bulgusu):
// migrate() -> beklemedeki kart insert edilir (published_at NULL) ->
// migrate() TEKRAR -> kart HÂLÂ beklemede olmalı. Sabit tarihe göre koşullu
// eski backfill bu ikinci migrate'te kartı sessizce yayınlıyordu.
func TestMigratePublishedBackfillOnlyOnce(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}

	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// t.Cleanup LIFO çalışır: Close burada ÖNCE kaydedildiği için, aşağıda
	// kaydedilen DELETE cleanup'ı Close'dan ÖNCE (havuz hâlâ açıkken) çalışır.
	// defer s.Close() kullanılsaydı fonksiyon dönüşünde t.Cleanup'lardan ÖNCE
	// tetiklenir, DELETE kapalı pool'a karşı denenip hatası sessizce yutulurdu.
	t.Cleanup(s.Close)

	id, err := s.InsertIdea(ctx, Idea{
		Title: "test-migrate-backfill-pending", ProblemStatement: "p", ProposedSolution: "s",
		TargetUser: "u", SourceType: "pain_point",
		UrgencyScore: 3,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id); err != nil {
			t.Logf("cleanup DELETE (id=%d): %v", id, err)
		}
	})

	// Insert sonrası kolon zaten var, kart beklemede (published_at NULL).
	var publishedAt *string
	if err := s.Pool.QueryRow(ctx, "SELECT published_at::text FROM ideas WHERE id = $1", id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil {
		t.Fatalf("insert sonrası kart beklemede olmalı (published_at NULL), geldi: %v", *publishedAt)
	}

	// İkinci Migrate: backfill kolon zaten var olduğundan çalışmamalı,
	// beklemedeki kart yayınlanmamalı.
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate: %v", err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT published_at::text FROM ideas WHERE id = $1", id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil {
		t.Errorf("ikinci Migrate beklemedeki kartı sessizce yayınlamamalı (published_at hâlâ NULL olmalı), geldi: %v", *publishedAt)
	}
}

// TestMigrateIdempotentWithAllSourceTypes, #131 hotfix regresyon testi:
// 004_market_derived.sql eskiden ideas_source_type_check kısıtını DAR bir
// listeyle (013_momentum_derived.sql'in GENİŞ listesini ezerek) yeniden
// tanımlıyordu. Tüm migration dosyaları her koşuda yeniden çalıştığından,
// üretimde bir momentum_derived satır oluştuğu an 004'ün DAR listesi o
// satırı ihlal ediyor ve zincir 013'e hiç ulaşmadan 004'te kırılıyordu —
// temiz bir test DB'sinde (satır yok) bu regresyon hiç ortaya çıkmazdı, bu
// yüzden mevcut testler yakalayamamıştı. Bu test her source_type
// değerinden (özellikle momentum_derived + ai_blended) birer satır VARKEN
// ikinci Migrate çağrısının hatasız geçtiğini doğrular.
func TestMigrateIdempotentWithAllSourceTypes(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}

	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// t.Cleanup LIFO çalışır: Close burada ÖNCE kaydedildiği için, aşağıda
	// kaydedilen DELETE cleanup'ı Close'dan ÖNCE (havuz hâlâ açıkken) çalışır.
	// defer s.Close() kullanılsaydı fonksiyon dönüşünde t.Cleanup'lardan ÖNCE
	// tetiklenir, DELETE kapalı pool'a karşı denenip hatası sessizce yutulurdu
	// (#139: test-migrate-source-type-* satırları DB'de kalıcı kalıyordu).
	t.Cleanup(s.Close)

	// ideas_source_type_check'in (013_momentum_derived.sql, TEK sahip —
	// bkz. migrations/README.md) kabul ettiği TÜM değerler; biri eksik
	// kalırsa insert burada patlar (kısıt zaten çalışıyor mu kontrolü).
	sourceTypes := []string{"pain_point", "ai_generated", "ai_blended", "market_derived", "user_created", "momentum_derived"}
	var ids []int64
	for _, st := range sourceTypes {
		id, err := s.InsertIdea(ctx, Idea{
			Title: "test-migrate-source-type-" + st, ProblemStatement: "p", ProposedSolution: "s",
			TargetUser: "u", SourceType: st, UrgencyScore: 3,
		})
		if err != nil {
			t.Fatalf("insert (source_type=%s): %v", st, err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			if _, err := s.Pool.Exec(ctx, "DELETE FROM ideas WHERE id = $1", id); err != nil {
				t.Logf("cleanup DELETE (id=%d): %v", id, err)
			}
		}
	})

	// İKİNCİ Migrate: 004 artık ideas_source_type_check'e DOKUNMUYOR (013
	// tek sahip) — momentum_derived dahil her source_type'tan satır varken
	// bile hatasız geçmeli. Hotfix öncesi bu adım "check constraint
	// ideas_source_type_check ... violated by some row" ile patlardı.
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate (her source_type'tan satır varken idempotent olmalı): %v", err)
	}
}

// TestMigrateThemePostsLinkedAtColumnAndDefault, 021_theme_posts_linked_at.sql
// (#189) doğrular: Migrate iki kez art arda hatasız çalışır; linked_at
// kolonu var ve DEFAULT'u now() (bir sonraki insert'i etkiler); LinkThemePost
// ile eklenen YENİ bir bağ linked_at NOT NULL alır — ADD COLUMN + SET
// DEFAULT'un iki ayrı ifade olarak (backfill YAPMADAN) uygulandığının
// kanıtı, aksi halde mevcut satırlar da now() ile dolar, backlog kaybolurdu.
func TestMigrateThemePostsLinkedAtColumnAndDefault(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil")
	}
	ctx := context.Background()

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ilk Migrate: %v", err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("ikinci Migrate (idempotent olmalı): %v", err)
	}

	s, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(s.Close)

	// Kolon var mı, DEFAULT'u now() ifadesini mi taşıyor.
	var columnDefault *string
	if err := s.Pool.QueryRow(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema = 'idealode' AND table_name = 'theme_posts' AND column_name = 'linked_at'`,
	).Scan(&columnDefault); err != nil {
		t.Fatalf("information_schema sorgusu: %v", err)
	}
	if columnDefault == nil || !strings.Contains(*columnDefault, "now()") {
		t.Fatalf("linked_at DEFAULT'u now() içermeli, geldi: %v", columnDefault)
	}

	// LinkThemePost ile eklenen YENİ satırda linked_at NOT NULL olmalı
	// (DEFAULT devreye girer).
	themeID, _, err := s.UpsertTheme(ctx, "test-migrate-linked-at-tag", "test-migrate-linked-at-tag")
	if err != nil {
		t.Fatalf("UpsertTheme: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM themes WHERE theme_name = 'test-migrate-linked-at-tag'")
	})

	if _, err := s.InsertRawPosts(ctx, []RawPost{
		{Platform: "test-migrate-linked-at", SourceRef: "p1", Community: "c", Title: "t"},
	}); err != nil {
		t.Fatalf("InsertRawPosts: %v", err)
	}
	t.Cleanup(func() {
		s.Pool.Exec(ctx, "DELETE FROM raw_posts WHERE platform = 'test-migrate-linked-at'")
	})

	var postID int64
	if err := s.Pool.QueryRow(ctx, "SELECT id FROM raw_posts WHERE platform = 'test-migrate-linked-at'").Scan(&postID); err != nil {
		t.Fatal(err)
	}

	if err := s.LinkThemePost(ctx, themeID, postID); err != nil {
		t.Fatalf("LinkThemePost: %v", err)
	}

	var linkedAt *string
	if err := s.Pool.QueryRow(ctx, "SELECT linked_at::text FROM theme_posts WHERE theme_id = $1 AND post_id = $2", themeID, postID).Scan(&linkedAt); err != nil {
		t.Fatal(err)
	}
	if linkedAt == nil {
		t.Error("LinkThemePost ile eklenen satırda linked_at NOT NULL olmalıydı (DEFAULT now() devreye girmeli)")
	}
}
