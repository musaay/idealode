package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ActiveSources, ingest'in işleyeceği aktif kaynakları döner.
func (s *Store) ActiveSources(ctx context.Context) ([]Source, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, platform, community, COALESCE(category, ''), active, COALESCE(last_seen_ref, '')
		FROM sources
		WHERE active
		ORDER BY platform, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Platform, &src.Community, &src.Category, &src.Active, &src.LastSeenRef); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// InsertRawPosts, post'ları idempotent yazar (UNIQUE(platform, source_ref)
// üzerinde ON CONFLICT DO NOTHING) ve gerçekten eklenen satır sayısını döner.
func (s *Store) InsertRawPosts(ctx context.Context, posts []RawPost) (int, error) {
	if len(posts) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, p := range posts {
		batch.Queue(`
			INSERT INTO raw_posts (platform, source_ref, community, title, body, author, url, score, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (platform, source_ref) DO NOTHING`,
			p.Platform, p.SourceRef, p.Community, p.Title, p.Body, p.Author, p.URL, p.Score, p.CreatedAt)
	}

	results := s.Pool.SendBatch(ctx, batch)
	defer results.Close()

	inserted := 0
	for range posts {
		tag, err := results.Exec()
		if err != nil {
			return inserted, fmt.Errorf("raw_posts insert: %w", err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

// RawPostExists, verilen platform+source_ref eşleşen bir raw_post olup
// olmadığını döner (tohum idempotency kontrolü, #56 — mark yazımından
// AYRI: bir tohumun daha önce "işlenip işlenmediğini" LLM'e hiç gitmeden
// anlamak için kullanılır; mark'ın kendisi InsertRawPosts ile ayrıca yazılır).
func (s *Store) RawPostExists(ctx context.Context, platform, sourceRef string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM raw_posts WHERE platform = $1 AND source_ref = $2)`,
		platform, sourceRef).Scan(&exists)
	return exists, err
}

// UnanalyzedPosts, henüz post_analysis kaydı olmayan post'ları döner
// (eskiden yeniye — imleç mantığıyla uyumlu). radar_seed ve github_trending
// platformları hariç tutulur: radar_seed satırları raw_posts'a yalnızca
// "işlendi" imleci olarak yazılır (#56); github_trending satırları ise
// LLM sinyal sınıflandırmasına değil, doğrudan füzyona (ivme kanıtı) girer
// (#50 B parçası) — ikisi de analiz kuyruğuna hiç girmemeli, pending
// sayacı da bu satırlarla şişmesin.
func (s *Store) UnanalyzedPosts(ctx context.Context, limit int) ([]RawPost, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.platform, rp.source_ref, rp.community, rp.title, rp.body,
		       COALESCE(rp.author, ''), COALESCE(rp.url, ''), COALESCE(rp.score, 0),
		       COALESCE(rp.created_at, rp.fetched_at)
		FROM raw_posts rp
		LEFT JOIN post_analysis pa ON pa.post_id = rp.id
		WHERE pa.id IS NULL
		  AND rp.platform NOT IN ('radar_seed', 'github_trending')
		ORDER BY rp.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		if err := rows.Scan(&p.ID, &p.Platform, &p.SourceRef, &p.Community, &p.Title,
			&p.Body, &p.Author, &p.URL, &p.Score, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertPostAnalyses, classification sonuçlarını idempotent yazar
// (post_id UNIQUE üzerinde ON CONFLICT DO NOTHING).
func (s *Store) InsertPostAnalyses(ctx context.Context, analyses []PostAnalysis) error {
	if len(analyses) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, a := range analyses {
		// nil slice pgx'te SQL NULL'a eşlenir ve NOT NULL kolonu ihlal
		// eder; çağıran ne gönderirse göndersin boş diziye indirgenir.
		tags := a.DomainTags
		if tags == nil {
			tags = []string{}
		}
		batch.Queue(`
			INSERT INTO post_analysis
				(post_id, classification, problem_summary, target_audience,
				 domain_tags, willingness_to_pay, prefiltered)
			VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7)
			ON CONFLICT (post_id) DO NOTHING`,
			a.PostID, a.Classification, a.ProblemSummary, a.TargetAudience,
			tags, a.WillingnessToPay, a.Prefiltered)
	}
	results := s.Pool.SendBatch(ctx, batch)
	defer results.Close()
	for range analyses {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("post_analysis insert: %w", err)
		}
	}
	return nil
}

// UnthemedAnalyses, henüz hiçbir temaya bağlanmamış sinyal analizlerini
// döner (yalnız pain_point / feature_request; noise/complaint tema kurmaz).
// Title/Body de döner (#127): kova içi LLM kümeleme prompt'u gönderi
// içeriğine ihtiyaç duyar (raw_posts JOIN'i).
func (s *Store) UnthemedAnalyses(ctx context.Context, limit int) ([]PostAnalysis, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT pa.post_id, pa.classification, pa.domain_tags, rp.title, rp.body
		FROM post_analysis pa
		JOIN raw_posts rp ON rp.id = pa.post_id
		WHERE pa.classification IN ('pain_point', 'feature_request')
		  AND COALESCE(array_length(pa.domain_tags, 1), 0) > 0
		  AND NOT EXISTS (SELECT 1 FROM theme_posts tp WHERE tp.post_id = pa.post_id)
		ORDER BY pa.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PostAnalysis
	for rows.Next() {
		var a PostAnalysis
		if err := rows.Scan(&a.PostID, &a.Classification, &a.DomainTags, &a.Title, &a.Body); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// runLockKey: `idealode run` koşuları için advisory lock anahtarı (#15).
const runLockKey int64 = 0x1DEA10DE

// AcquireRunLock, koşu kilidi almayı dener (bloklamadan). Kilit alınamazsa
// ok=false döner — başka bir koşu (örn. çakışan cron) devam ediyordur.
// Kilit, release çağrılana dek pool'dan ayrılmış tek bağlantıda tutulur.
func (s *Store) AcquireRunLock(ctx context.Context) (release func(), ok bool, err error) {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", runLockKey).Scan(&got); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	release = func() {
		// Bağlantı kapanınca kilit zaten düşer; yine de düzgünce bırak.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", runLockKey)
		conn.Release()
	}
	return release, true, nil
}

// IdeasNeedingFusion, füzyon denenmemiş ya da haftadan eski füzyonu olan
// market_derived kartları döner (fused_at boş önce). Haftalık yeniden
// deneme yalnız ivme geçişini (fuseMomentum) tetikler — talep hakemi
// (fuseJudge) tekrar çağrılmaz, LLM maliyeti tekrar ödenmez (#50 B parçası).
func (s *Store) IdeasNeedingFusion(ctx context.Context, limit int) ([]Idea, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, title, problem_statement, proposed_solution, domain_tags, fused_at
		FROM ideas
		WHERE source_type = 'market_derived'
		  AND (fused_at IS NULL OR fused_at < now() - interval '7 days')
		ORDER BY fused_at NULLS FIRST, id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Idea
	for rows.Next() {
		var i Idea
		if err := rows.Scan(&i.ID, &i.Title, &i.ProblemStatement, &i.ProposedSolution, &i.DomainTags, &i.FusedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CountIdeasSince, belirli source_type'tan son `sinceDays` gün içinde açılan
// kart sayısını döner — ivme kartlarının haftalık sert tavanı için (#89):
// çağıran sinceDays=7, source_type='momentum_derived' geçer; sonuç ≥1 ise
// tohum bu koşuda atlanır (imleç yazılmadan).
func (s *Store) CountIdeasSince(ctx context.Context, sourceType string, sinceDays int) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM ideas
		 WHERE source_type = $1 AND created_at >= now() - make_interval(days => $2)`,
		sourceType, sinceDays).Scan(&n)
	return n, err
}

// TrendingPersistenceDays, github_trending kaynağındaki bir repo'nun son
// `sinceDays` gün içinde raw_posts'ta kaç FARKLI günde göründüğünü
// (kalıcılık, #89 ivme kapısı madde 1) ve en son görüldüğü günün skorunu
// (günlük ★ artışı, evidence metninde "+M/gün" için) döner. repo,
// "owner/repo" biçiminde — source_ref "owner/repo:YYYY-MM-DD" olarak
// tutulur (bkz. connector.GitHubTrending.FetchNew).
func (s *Store) TrendingPersistenceDays(ctx context.Context, repo string, sinceDays int) (persistedDays int, lastDailyDelta int, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT
			count(DISTINCT split_part(source_ref, ':', 2)),
			coalesce((
				SELECT score FROM raw_posts
				WHERE platform = 'github_trending' AND split_part(source_ref, ':', 1) = $1
				ORDER BY created_at DESC LIMIT 1
			), 0)
		FROM raw_posts
		WHERE platform = 'github_trending'
		  AND split_part(source_ref, ':', 1) = $1
		  AND created_at >= now() - make_interval(days => $2)`,
		repo, sinceDays).Scan(&persistedDays, &lastDailyDelta)
	return persistedDays, lastDailyDelta, err
}

// FusionCandidates, karta aday yerel talep kanıtlarını döner: tag kesişimi
// ya da başlık benzerliği olan, sinyalli (pain_point/feature_request)
// post'lar, benzerliğe göre sıralı.
func (s *Store) FusionCandidates(ctx context.Context, tags []string, problem string, limit int) ([]RawPost, error) {
	if tags == nil {
		tags = []string{}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.platform, p.community, p.title, p.body, p.url,
		       GREATEST(similarity(p.title, $2), CASE WHEN a.domain_tags && $1 THEN 0.5 ELSE 0 END) AS sim
		FROM raw_posts p
		JOIN post_analysis a ON a.post_id = p.id
		WHERE a.classification IN ('pain_point', 'feature_request')
		  AND (a.domain_tags && $1 OR similarity(p.title, $2) > 0.15)
		ORDER BY sim DESC, p.id DESC
		LIMIT $3`, tags, problem, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		var sim float64
		if err := rows.Scan(&p.ID, &p.Platform, &p.Community, &p.Title, &p.Body, &p.URL, &sim); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetIdeaLocalEvidence, füzyon sonucunu karta yazar; kanıt bulunamasa da
// fused_at damgalanır (her koşuda yeniden denenmesin).
func (s *Store) SetIdeaLocalEvidence(ctx context.Context, ideaID int64, evidence []string) error {
	if evidence == nil {
		evidence = []string{}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE ideas SET local_evidence = $2, fused_at = now(), updated_at = now()
		WHERE id = $1`, ideaID, evidence)
	return err
}

// MomentumCandidates, karta aday GitHub ivme repolarını döner: son 7 günün
// github_trending satırları arasından repo başına en güncel satır (aynı
// repo farklı günlerde tekrar edebilir), problem metnine trigram
// benzerliği düşük bir eşiği (0.10 — title yalnız "owner/repo" olduğundan
// FusionCandidates'takinden düşük tutulur) geçenler, benzerliğe göre
// sıralı. tags şu an filtrede kullanılmıyor (repo'nun kendi domain_tags'i
// yok); imza FusionCandidates ile simetri ve ileride tag-tabanlı daraltma
// için ayrılmıştır.
func (s *Store) MomentumCandidates(ctx context.Context, problem string, tags []string, limit int) ([]RawPost, error) {
	if tags == nil {
		tags = []string{}
	}
	rows, err := s.Pool.Query(ctx, `
		WITH recent AS (
			SELECT DISTINCT ON (split_part(source_ref, ':', 1))
			       id, platform, source_ref, community, title, body,
			       COALESCE(author, '') AS author, COALESCE(url, '') AS url,
			       COALESCE(score, 0) AS score, created_at
			FROM raw_posts
			WHERE platform = 'github_trending'
			  AND created_at > now() - interval '7 days'
			ORDER BY split_part(source_ref, ':', 1), created_at DESC
		)
		SELECT id, platform, source_ref, community, title, body, author, url, score, created_at,
		       similarity(title || ' ' || body, $1) AS sim
		FROM recent
		WHERE similarity(title || ' ' || body, $1) > 0.10
		ORDER BY sim DESC
		LIMIT $2`, problem, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		var sim float64
		if err := rows.Scan(&p.ID, &p.Platform, &p.SourceRef, &p.Community, &p.Title,
			&p.Body, &p.Author, &p.URL, &p.Score, &p.CreatedAt, &sim); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AppendIdeaLocalEvidence, mevcut yerel talep kanıt satırlarını SİLMEDEN
// yeni satırlar ekler (füzyonun ivme geçişi — SetIdeaLocalEvidence'ın
// aksine diziyi bütünüyle değiştirmez). fused_at her çağrıda damgalanır
// (satır eklenmese bile — haftalık yeniden deneme penceresi için "denendi"
// işareti gerekir). nil slice pgx'te SQL NULL yazar ve `||` birleştirmesini
// kırar; guard ile boş diziye indirgenir (CLAUDE.md nil-slice tuzağı).
func (s *Store) AppendIdeaLocalEvidence(ctx context.Context, ideaID int64, lines []string) error {
	if lines == nil {
		lines = []string{}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE ideas SET local_evidence = local_evidence || $2, fused_at = now(), updated_at = now()
		WHERE id = $1`, ideaID, lines)
	return err
}

// FindSimilarIdea, verilen başlık+probleme pg_trgm ile en çok benzeyen
// mevcut kartı ve benzerlik skorunu döner (kart yoksa sim=0, id=0).
func (s *Store) FindSimilarIdea(ctx context.Context, title, problem string) (Idea, float64, error) {
	var i Idea
	var sim float64
	err := s.Pool.QueryRow(ctx, `
		SELECT id, title, problem_statement, evidence_count,
		       GREATEST(similarity(title, $1), similarity(problem_statement, $2)) AS sim
		FROM ideas
		ORDER BY sim DESC
		LIMIT 1`, title, problem).Scan(&i.ID, &i.Title, &i.ProblemStatement, &i.EvidenceCount, &sim)
	if err == pgx.ErrNoRows {
		return Idea{}, 0, nil
	}
	if err != nil {
		return Idea{}, 0, err
	}
	return i, sim, nil
}

// MergeIdeaEvidence, mükerrer temanın kanıtını mevcut karta ekler.
func (s *Store) MergeIdeaEvidence(ctx context.Context, ideaID int64, addCount int) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE ideas SET evidence_count = evidence_count + $2, updated_at = now()
		WHERE id = $1`, ideaID, addCount)
	return err
}

// MarkThemeMerged, temayı mevcut bir karta katılmış olarak işaretler;
// işaretli tema senteze yeniden girmez.
func (s *Store) MarkThemeMerged(ctx context.Context, themeID, ideaID int64) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE themes SET merged_into_idea_id = $2 WHERE id = $1`, themeID, ideaID)
	return err
}

// MarkThemeIncoherent, tutarlılık denetimini geçemeyen temayı işaretler;
// tema, yeni kanıt gelene dek (last_seen > incoherent_at) senteze girmez.
func (s *Store) MarkThemeIncoherent(ctx context.Context, themeID int64) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE themes SET incoherent_at = now() WHERE id = $1`, themeID)
	return err
}

// UpsertTheme, tag için temayı bulur/oluşturur ve last_seen'i tazeler.
// domainTag, temanın doğduğu kaba kova (#127) — yalnız İLK insert'te
// yazılır; çakışmada (tema zaten varsa) domain_tag DOKUNULMADAN kalır
// (aynı tema adı iki farklı kovadan gelirse ilk yazan kovanınki geçerli
// kalır — çakışmada UPDATE yok).
func (s *Store) UpsertTheme(ctx context.Context, name, domainTag string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO themes (theme_name, domain_tag) VALUES ($1, $2)
		ON CONFLICT (theme_name) DO UPDATE SET last_seen = now()
		RETURNING id`, name, domainTag).Scan(&id)
	return id, err
}

// LinkThemePost, post'u temaya idempotent bağlar.
func (s *Store) LinkThemePost(ctx context.Context, themeID, postID int64) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO theme_posts (theme_id, post_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, themeID, postID)
	return err
}

// dbExecutor, hem *pgxpool.Pool hem pgx.Tx'in ortak üç metodu — retheme'in
// tek transaction'lı yazan yolu (RethemeResolve) ile salt-okunur yolun
// (RethemeCandidates/RethemeCandidateCount, dry-run) aynı sorgu
// yardımcılarını (rethemeTargets, refreshThemeStats) paylaşabilmesi için
// (#136).
type dbExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// refreshThemeStats, frequency'yi theme_posts sayısıyla eşitler. LEFT JOIN
// kasıtlı: theme_posts'ta hiç satırı kalmayan bir tema da 0'a çekilmeli
// (retheme bağları sildiğinde bu durum oluşur, #136) — eski INNER JOIN
// deseni (yalnız count subquery'sinde eşleşen satırları güncelleme) bu
// temaları eski frekansında BIRAKIRDI. GroupThemes yalnız EKLEME yaptığından
// (post sayısı hiç 0'a düşmez) bu düzeltme onun davranışını değiştirmez.
func refreshThemeStats(ctx context.Context, db dbExecutor) error {
	_, err := db.Exec(ctx, `
		UPDATE themes t
		SET frequency = COALESCE(c.cnt, 0)
		FROM themes t2
		LEFT JOIN (SELECT theme_id, count(*) AS cnt FROM theme_posts GROUP BY theme_id) c
		  ON c.theme_id = t2.id
		WHERE t2.id = t.id`)
	return err
}

// RefreshThemeStats, frequency'yi theme_posts sayısıyla eşitler.
func (s *Store) RefreshThemeStats(ctx context.Context) error {
	return refreshThemeStats(ctx, s.Pool)
}

// ------------------------------------------------------------- retheme (#136)
//
// Eski tip temalarda (theme_name == domain_tag ya da domain_tag NULL) —
// eşiği geçmiş ama hiçbir zaman karta dönüşmemiş — biriken gönderiler her
// koşuda tutarlılık kontrolünden düşüp kart üretmeden yer kaplıyordu.
// `idealode retheme` bu gönderileri eski temalarından ÇÖZER (theme_posts
// bağını siler); yeniden kümelemeyi YAPMAZ — GroupThemes bir sonraki
// koşuda UnthemedAnalyses ile bu gönderileri temasız bulup LLM
// kümelemesinden geçirir. Eski tema satırı SİLİNMEZ (frekansı 0'a
// düşebilir, zararsız, geri izlenebilir).

// RethemeTarget, retheme hedef kümesindeki TEK theme_posts bağı.
type RethemeTarget struct {
	ThemeID   int64
	ThemeName string
	PostID    int64
}

// RethemeThemeSummary, dry-run raporundaki "ilk 10 tema" satırı.
type RethemeThemeSummary struct {
	Name      string
	Frequency int
}

// rethemeTargetsWhere, hedef kümenin ortak WHERE koşulu: eski tip tema
// (domain_tag NULL ya da theme_name == domain_tag) VE frequency eşiği VE
// bu temaya bağlı idea YOK (kartlı temalar asla dokunulmaz).
const rethemeTargetsWhere = `
	(t.domain_tag IS NULL OR t.theme_name = t.domain_tag)
	AND t.frequency >= $1
	AND NOT EXISTS (SELECT 1 FROM ideas i WHERE i.source_theme_id = t.id)`

// rethemeTargets, hedef kümedeki theme_posts bağlarını (tema id, post id)
// sırasıyla en fazla `limit` GÖNDERİ ile döner (#136) — seçim sırası
// belirli/tekrarlanabilir. db, hem Pool (salt-okunur: dry-run/sayım) hem Tx
// (yazan yol: RethemeResolve, atomiklik için) olabilir.
func rethemeTargets(ctx context.Context, db dbExecutor, minEvidence, limit int) ([]RethemeTarget, error) {
	rows, err := db.Query(ctx, `
		SELECT tp.theme_id, t.theme_name, tp.post_id
		FROM theme_posts tp
		JOIN themes t ON t.id = tp.theme_id
		WHERE `+rethemeTargetsWhere+`
		ORDER BY tp.theme_id, tp.post_id
		LIMIT $2`, minEvidence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RethemeTarget
	for rows.Next() {
		var t RethemeTarget
		if err := rows.Scan(&t.ThemeID, &t.ThemeName, &t.PostID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RethemeCandidates, hedef kümedeki theme_posts bağlarını (limit uygulanmış,
// belirli sırada) salt-okunur döner — dry-run önizlemesi için.
func (s *Store) RethemeCandidates(ctx context.Context, minEvidence, limit int) ([]RethemeTarget, error) {
	return rethemeTargets(ctx, s.Pool, minEvidence, limit)
}

// RethemeCandidateCount, hedef kümedeki TOPLAM (limit'siz) theme_posts bağı
// sayısını döner — "kalan" hesaplaması ve idempotent boş-küme kontrolü için.
func (s *Store) RethemeCandidateCount(ctx context.Context, minEvidence int) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM theme_posts tp
		JOIN themes t ON t.id = tp.theme_id
		WHERE `+rethemeTargetsWhere, minEvidence).Scan(&n)
	return n, err
}

// RethemeTopThemes, hedef kümedeki temalardan İLK n tanesini (tema id
// sırasıyla — rethemeTargets'taki seçim sırasıyla aynı) ad + mevcut
// frequency ile döner (dry-run raporu).
func (s *Store) RethemeTopThemes(ctx context.Context, minEvidence, n int) ([]RethemeThemeSummary, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.theme_name, t.frequency
		FROM themes t
		WHERE `+rethemeTargetsWhere+`
		ORDER BY t.id
		LIMIT $2`, minEvidence, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RethemeThemeSummary
	for rows.Next() {
		var t RethemeThemeSummary
		if err := rows.Scan(&t.Name, &t.Frequency); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RethemeWontReclusterCount, verilen post id'lerinden kaçının, temasından
// çözüldükten SONRA UnthemedAnalyses kriterini (sinyalli sınıflandırma +
// dolu domain_tags) SAĞLAMAYACAĞINI döner — bunlar bir sonraki GroupThemes
// çağrısında yeniden kümelenemeyecek, temasız kalacak (dry-run raporu,
// spec edge case).
func (s *Store) RethemeWontReclusterCount(ctx context.Context, postIDs []int64) (int, error) {
	if len(postIDs) == 0 {
		return 0, nil
	}
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM unnest($1::bigint[]) AS pid
		LEFT JOIN post_analysis pa ON pa.post_id = pid
		WHERE pa.post_id IS NULL
		   OR pa.classification NOT IN ('pain_point', 'feature_request')
		   OR COALESCE(array_length(pa.domain_tags, 1), 0) = 0`, postIDs).Scan(&n)
	return n, err
}

// RethemeResolve, hedef kümedeki en fazla `limit` GÖNDERİYİ TEK transaction
// içinde temalarından çözer: seçilen theme_posts satırları silinir, ardından
// refreshThemeStats aynı transaction'da çağrılır (atomiklik — yarıda
// kesilirse hiçbir bağ silinmemiş sayılır). Eski tema satırı SİLİNMEZ.
// resolved: çözülen gönderi sayısı, themes: etkilenen FARKLI tema sayısı.
func (s *Store) RethemeResolve(ctx context.Context, minEvidence, limit int) (resolved, themes int, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Rollback'te DIŞARIDAN gelen ctx DEĞİL context.Background() kullanılır:
	// hata/timeout/iptal zaten bu ctx'ten geldiyse aynı ctx ile ROLLBACK
	// göndermeye çalışmak da başarısız olabilir — bağlantı sunucuya
	// ROLLBACK göndermeden kopar, transaction sunucu tarafında açık
	// kalabilir (pool bağlantıyı sağlıksız sayıp kapatana dek). Commit
	// sonrası bu çağrı no-op'tur (zaten kapalı tx).
	defer func() { _ = tx.Rollback(context.Background()) }()

	targets, err := rethemeTargets(ctx, tx, minEvidence, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("retheme hedef kümesi: %w", err)
	}
	if len(targets) == 0 {
		return 0, 0, tx.Commit(ctx)
	}

	batch := &pgx.Batch{}
	affected := map[int64]bool{}
	for _, t := range targets {
		batch.Queue(`DELETE FROM theme_posts WHERE theme_id = $1 AND post_id = $2`, t.ThemeID, t.PostID)
		affected[t.ThemeID] = true
	}
	br := tx.SendBatch(ctx, batch)
	for range targets {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return 0, 0, fmt.Errorf("theme_posts silme: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return 0, 0, fmt.Errorf("theme_posts silme (batch kapanışı): %w", err)
	}

	if err := refreshThemeStats(ctx, tx); err != nil {
		return 0, 0, fmt.Errorf("RefreshThemeStats: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return len(targets), len(affected), nil
}

// ThemesByDomainTag, verilen kovada (domain_tag) daha önce oluşturulmuş
// temaları döner — kova içi LLM kümelemesine "mevcut tema adları" bağlamı
// olarak verilir (#127). domain_tag DB'de nullable (eski satırlar migration
// backfill'i ile dolar); COALESCE savunma amaçlı (ActiveSources'taki
// category deseniyle aynı).
func (s *Store) ThemesByDomainTag(ctx context.Context, domainTag string) ([]Theme, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, theme_name, frequency, COALESCE(domain_tag, '')
		FROM themes
		WHERE domain_tag = $1
		ORDER BY id`, domainTag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Theme
	for rows.Next() {
		var t Theme
		if err := rows.Scan(&t.ID, &t.Name, &t.Frequency, &t.DomainTag); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThemesReadyForSynthesis, frekans eşiğini geçmiş ve henüz idea üretilmemiş
// temaları döner. Kümelenmiş temalar (#135 — gerçek dert kümelemesinden
// doğan, #127) HER ZAMAN öne alınır: eski etiket temaları (theme_name ==
// domain_tag) yıllarca birikmiş post taşıdığı için frekansta doğal olarak
// üstün çıkıyor ve yeni dert temalarını sentez sırasına hiç sokmuyordu.
// preferPayment açıkken (#125 — önceki sert eleme #121'de eklenmişti,
// üretimde kart akışını sıfıra düşürdüğü için kaldırıldı) ödeme sinyali
// taşıyan temalar (en az bir postu willingness_to_pay=true) ikincil anahtar
// olarak öne alınır: ORDER BY clustered DESC, has_payment_signal DESC,
// t.frequency DESC, t.id. Her iki boolean da tek geçişte SELECT'te
// hesaplanır, ORDER BY bu kolonları (alias) referanslar — tekrar
// hesaplanmaz. preferPayment kapalıyken ödeme anahtarı eklenmez ama
// kümelenmiş anahtarı yine eklenir — bu bir tercih değil, doğruluk meselesi.
//
// Edge case: domain_tag NULL ise (017 backfill'inden önce/dışında kalmış
// bir satır varsa) düz "theme_name IS DISTINCT FROM domain_tag" NULL'ı
// yanlış ele alır (NULL'dan farklı her şey true döner, tema hatalı biçimde
// "yeni tip" sayılır) — bu yüzden "domain_tag IS NOT NULL" guard'ı şart;
// NULL açıkça "eski tip" sayılır.
// Edge case: LLM bir kümeyi tesadüfen kovanın adıyla adlandırırsa
// (theme_name == domain_tag) o tema da "eski tip" sayılır ve öncelik almaz
// — zararsız, kendini düzeltir (temanın ismi sonraki kümelemede değişebilir).
func (s *Store) ThemesReadyForSynthesis(ctx context.Context, minEvidence, limit int, preferPayment bool) ([]Theme, error) {
	query := `
		SELECT t.id, t.theme_name, t.frequency,
		       (t.domain_tag IS NOT NULL AND t.theme_name IS DISTINCT FROM t.domain_tag) AS clustered,
		       EXISTS (
		           SELECT 1 FROM theme_posts tp
		           JOIN post_analysis pa ON pa.post_id = tp.post_id
		           WHERE tp.theme_id = t.id AND pa.willingness_to_pay
		       ) AS has_payment_signal
		FROM themes t
		WHERE t.frequency >= $1
		  AND NOT EXISTS (SELECT 1 FROM ideas i WHERE i.source_theme_id = t.id)
		  AND t.merged_into_idea_id IS NULL
		  AND (t.incoherent_at IS NULL OR t.last_seen > t.incoherent_at)
		ORDER BY clustered DESC, `
	if preferPayment {
		query += `has_payment_signal DESC, `
	}
	query += `t.frequency DESC, t.id
		LIMIT $2`

	rows, err := s.Pool.Query(ctx, query, minEvidence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Theme
	for rows.Next() {
		var t Theme
		if err := rows.Scan(&t.ID, &t.Name, &t.Frequency, &t.Clustered, &t.HasPaymentSignal); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThemeEvidence, temayı destekleyen ham post'ları döner (en yüksek skor önce).
func (s *Store) ThemeEvidence(ctx context.Context, themeID int64, limit int) ([]RawPost, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT rp.id, rp.platform, rp.source_ref, rp.community, rp.title, rp.body,
		       COALESCE(rp.author, ''), COALESCE(rp.url, ''), COALESCE(rp.score, 0),
		       COALESCE(rp.created_at, rp.fetched_at)
		FROM theme_posts tp
		JOIN raw_posts rp ON rp.id = tp.post_id
		WHERE tp.theme_id = $1
		ORDER BY rp.score DESC NULLS LAST, rp.id DESC
		LIMIT $2`, themeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RawPost
	for rows.Next() {
		var p RawPost
		if err := rows.Scan(&p.ID, &p.Platform, &p.SourceRef, &p.Community, &p.Title,
			&p.Body, &p.Author, &p.URL, &p.Score, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IdeaTitleExists, birebir başlık eşleşmesine bakar (Faz 0 naif dedup;
// pg_trgm benzerlik dedup'u Faz 1).
func (s *Store) IdeaTitleExists(ctx context.Context, title string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ideas WHERE lower(title) = lower($1))`, title).Scan(&exists)
	return exists, err
}

// maxSlugAttempts, InsertIdea/InsertBlendedIdea'nın slug UNIQUE çakışmasında
// yeni bir rastgele ekle yeniden deneme üst sınırı (#110 spec).
const maxSlugAttempts = 3

// InsertIdea, idea card'ı yazar ve id döner. Slug başlıktan türetilir
// (slugify + rastgele ek); UNIQUE çakışmasında yeni ek ile en fazla
// maxSlugAttempts kez denenir.
func (s *Store) InsertIdea(ctx context.Context, i Idea) (int64, error) {
	// nil slice'lar SQL NULL'a eşlenir; NOT NULL kolonlar için boş diziye
	// indirgenir (bkz. InsertPostAnalyses'teki aynı koruma).
	if i.DomainTags == nil {
		i.DomainTags = []string{}
	}
	if i.ExampleQuotes == nil {
		i.ExampleQuotes = []string{}
	}

	var id int64
	var err error
	for attempt := 0; attempt < maxSlugAttempts; attempt++ {
		slug := newSlug(i.Title)
		err = s.Pool.QueryRow(ctx, `
			INSERT INTO ideas
				(title, slug, problem_statement, proposed_solution, target_user, evidence_count,
				 example_quotes, source_type, source_theme_id, created_by_user_id,
				 urgency_score, monetization_signal, known_competitors_ai_guess, domain_tags,
				 distinctiveness_verdict, distinctiveness_criterion, distinctiveness_reason,
				 data_access_verdict, data_access_reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), $14, $15, $16, $17, $18, $19)
			RETURNING id`,
			i.Title, slug, i.ProblemStatement, i.ProposedSolution, i.TargetUser, i.EvidenceCount,
			i.ExampleQuotes, i.SourceType, i.SourceThemeID, nil,
			i.UrgencyScore, i.MonetizationSignal, i.KnownCompetitorsAIGuess, i.DomainTags,
			i.DistinctivenessVerdict, i.DistinctivenessCriterion, i.DistinctivenessReason,
			i.DataAccessVerdict, i.DataAccessReason).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !isSlugConflict(err) {
			return 0, fmt.Errorf("ideas insert: %w", err)
		}
	}
	return 0, fmt.Errorf("ideas insert: slug %d denemede benzersiz üretilemedi: %w", maxSlugAttempts, err)
}

// ListIdeas, idea card'ları tema adıyla birlikte döner (yeniden eskiye) —
// Faz 0'da `dump` komutu ve kalite kapısı değerlendirmesi bununla beslenir.
func (s *Store) ListIdeas(ctx context.Context, limit int) ([]Idea, error) {
	rows, err := s.Pool.Query(ctx, ideaSelect+`
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Idea
	for rows.Next() {
		var i Idea
		if err := scanIdea(rows, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// PendingIdea, PendingIdeas'ın döndürdüğü hafif özet satırı — `idealode run`
// log özeti için id+title+özgünlük kararı (#102, #108) + veri-erişimi kararı
// (#131 — reason taşınmaz, distinctiveness_criterion'un aksine gösterimde
// yalnız verdict kelimesi kullanılır, bkz. pendingIdeaSummary).
type PendingIdea struct {
	ID                       int64
	Title                    string
	DistinctivenessVerdict   *string
	DistinctivenessCriterion *string
	DataAccessVerdict        *string
}

// PendingIdeas, henüz PO onayı almamış (published_at IS NULL) ve arşivde
// olmayan kartları döner — `idealode run` sonunda "beklemede N kart"
// özetini beslemek için (#102 moderasyon kuyruğu). Onay şimdilik sözlü:
// lead DB'den `UPDATE ideas SET published_at = now() WHERE id = ...` ile
// açar.
func (s *Store) PendingIdeas(ctx context.Context) ([]PendingIdea, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, title, distinctiveness_verdict, distinctiveness_criterion, data_access_verdict FROM ideas
		WHERE published_at IS NULL AND archived_at IS NULL
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingIdea
	for rows.Next() {
		var p PendingIdea
		if err := rows.Scan(&p.ID, &p.Title, &p.DistinctivenessVerdict, &p.DistinctivenessCriterion, &p.DataAccessVerdict); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateSourceLastSeen, kaynağın ilerleme imlecini günceller.
func (s *Store) UpdateSourceLastSeen(ctx context.Context, sourceID int64, ref string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE sources SET last_seen_ref = $2, updated_at = now() WHERE id = $1`,
		sourceID, ref)
	return err
}

// ErrNotFound, tekil kayıt sorgularının (GetIdea) kayıt bulunamadığında
// döndürdüğü sentinel hata — web katmanı bunu 404'e çevirir.
var ErrNotFound = errors.New("kayıt bulunamadı")

// IdeaFilter, galeri listelemesinin filtreleri. Sıfır değer = filtresiz.
type IdeaFilter struct {
	SourceType string // boş = hepsi
	Query      string // title/problem_statement ILIKE araması; boş = arama yok
	Limit      int    // <= 0 ise DefaultIdeaLimit
	SessionID  string // ai_blended görünürlük kuralı için istek sahibinin oturumu
	Flag       string // boş = hepsi; FlagDoubtful = özgünlük merceği şüpheli bulmuş
}

// FlagDoubtful, IdeaFilter.Flag'in tek geçerli değeri: özgünlük merceğinin
// (#101, advisory) fail ya da unsure dediği kartlar. Beyaz liste burada
// tektir — api, apiclient ve web aynı sabite bakar.
const FlagDoubtful = "doubtful"

// DefaultIdeaLimit, galeri listelemesinin varsayılan üst sınırı.
const DefaultIdeaLimit = 60

// maxIdeaLimit, dışarıdan gelen limit'in tavanı (kaynak koruması).
const maxIdeaLimit = 200

// ideaSelect, Idea satırını okuyan ortak SELECT gövdesi. Kolon sırası
// scanIdea ile birebir eşleşir; iki yerde değiştirilmelidir. pi (parent
// ideas) self-join'i yalnız ai_blended kartın kaynak kart bağlantısını
// slug'la kurabilmek için (#110) — parent yoksa parent_slug "" döner.
const ideaSelect = `
	SELECT i.id, i.slug, i.title, i.problem_statement, i.proposed_solution, i.target_user,
	       i.evidence_count, i.example_quotes, i.source_type, i.source_theme_id,
	       COALESCE(i.urgency_score, 0), COALESCE(i.monetization_signal, 0),
	       COALESCE(i.known_competitors_ai_guess, ''), i.domain_tags,
	       i.local_evidence, i.parent_idea_id, COALESCE(pi.slug, ''), COALESCE(i.created_by_session_id, ''),
	       COALESCE(t.theme_name, ''), i.created_at,
	       i.distinctiveness_verdict, i.distinctiveness_criterion, i.distinctiveness_reason,
	       i.data_access_verdict, i.data_access_reason,
	       i.published_at
	FROM ideas i
	LEFT JOIN themes t ON t.id = i.source_theme_id
	LEFT JOIN ideas pi ON pi.id = i.parent_idea_id`

// scanIdea, ideaSelect kolon sırasını Idea'ya okur ve nil slice'ları boş
// diziye indirger (şablon `range` ve len() güvenliği). Mine alanı burada
// DOLDURULMAZ — çağıran (oturuma göre) ayrıca hesaplar (bkz. setMine).
func scanIdea(row pgx.Row, i *Idea) error {
	if err := row.Scan(&i.ID, &i.Slug, &i.Title, &i.ProblemStatement, &i.ProposedSolution,
		&i.TargetUser, &i.EvidenceCount, &i.ExampleQuotes, &i.SourceType,
		&i.SourceThemeID, &i.UrgencyScore, &i.MonetizationSignal,
		&i.KnownCompetitorsAIGuess, &i.DomainTags, &i.LocalEvidence,
		&i.ParentIdeaID, &i.ParentSlug, &i.CreatedBySessionID,
		&i.SourceTheme, &i.CreatedAt,
		&i.DistinctivenessVerdict, &i.DistinctivenessCriterion, &i.DistinctivenessReason,
		&i.DataAccessVerdict, &i.DataAccessReason,
		&i.PublishedAt); err != nil {
		return err
	}
	if i.ExampleQuotes == nil {
		i.ExampleQuotes = []string{}
	}
	if i.DomainTags == nil {
		i.DomainTags = []string{}
	}
	if i.LocalEvidence == nil {
		i.LocalEvidence = []string{}
	}
	return nil
}

// setMine, ai_blended kart görünürlük kuralını uygular: Mine yalnız
// source_type='ai_blended' VE created_by_session_id oturumla eşleşiyorsa
// true olur. Diğer kart türlerinde her zaman false (herkese açık).
func setMine(i *Idea, sid string) {
	i.Mine = i.SourceType == "ai_blended" && sid != "" && i.CreatedBySessionID == sid
}

// ListIdeasFiltered, galeri listesini kaynak türü ve serbest metin
// filtresiyle döner (yeniden eskiye). Filtreler SQL parametresi olarak
// geçer; ILIKE deseni kaçışlanır.
func (s *Store) ListIdeasFiltered(ctx context.Context, f IdeaFilter) ([]Idea, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultIdeaLimit
	}
	if limit > maxIdeaLimit {
		limit = maxIdeaLimit
	}

	// $5 (Flag) beyaz listedir: yalnız FlagDoubtful tanınır, başka her değer
	// filtresiz sayılır — sorguya ham değer geçmez.
	flag := ""
	if f.Flag == FlagDoubtful {
		flag = FlagDoubtful
	}

	// $1 boşsa kaynak türü filtresi devre dışı; $2 boşsa arama devre dışı.
	// $4 (SessionID): ai_blended kart yalnız üreten oturuma görünür — herkese
	// açık galeriye anonim kart girmez (doğrulanmışlık ilkesi).
	// published_at IS NOT NULL: moderasyon kuyruğu (#102) — pipeline'ın
	// ürettiği kart PO onaylayıp published_at'i doldurana dek galeride
	// görünmez. ai_blended kartlar InsertBlendedIdea'da published_at=now()
	// yazdığından (zaten yalnız sahibine görünüyorlardı) bu şart onları
	// etkilemez.
	q := ideaSelect + `
		WHERE ($1 = '' OR i.source_type = $1)
		  AND ($2 = '' OR i.title ILIKE $2 OR i.problem_statement ILIKE $2
		       OR i.proposed_solution ILIKE $2)
		  AND (i.source_type <> 'ai_blended' OR i.created_by_session_id = $4)
		  AND i.archived_at IS NULL
		  AND i.published_at IS NOT NULL
		  AND ($5 = '' OR i.distinctiveness_verdict IN ('fail', 'unsure'))
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $3`

	pattern := ""
	if t := strings.TrimSpace(f.Query); t != "" {
		pattern = "%" + escapeLike(t) + "%"
	}

	rows, err := s.Pool.Query(ctx, q, f.SourceType, pattern, limit, f.SessionID, flag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Idea{}
	for rows.Next() {
		var i Idea
		if err := scanIdea(rows, &i); err != nil {
			return nil, err
		}
		setMine(&i, f.SessionID)
		out = append(out, i)
	}
	return out, rows.Err()
}

// escapeLike, ILIKE deseninde joker karakterleri etkisizleştirir; desen
// varsayılan ESCAPE ('\') ile yorumlanır.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// getIdeaWhere, GetIdea/GetIdeaBySlug'ın ortak gövdesi: tek eşitlik
// koşuluyla satırı çeker ve görünürlük kuralını uygular. Görünürlük kuralı:
// başkasının ai_blended kartı da ErrNotFound döner (var olduğu sızdırılmaz,
// 404 — bkz. spec kabul kriteri 4). Arşivlenmiş kart (archived_at doluysa)
// da aynı şekilde ErrNotFound döner — galeriden kaldırılan kart erişilemez
// olmalı (#74). Beklemedeki (published_at NULL) kart da aynı şekilde
// ErrNotFound döner (#102 moderasyon kuyruğu — varlığı sızdırılmaz);
// IdeaSources/ListChat/AppendChat/InsertBlendedIdea çağrıları hep önce
// GetIdea(BySlug)'dan geçtiğinden (bkz. internal/api/handlers.go) bu tek
// nokta sohbet/kaynak/blend uçlarını da otomatik kapatır — ayrı kontrol
// gerekmez.
func (s *Store) getIdeaWhere(ctx context.Context, cond string, arg any, sid string) (*Idea, error) {
	var i Idea
	err := scanIdea(s.Pool.QueryRow(ctx, ideaSelect+` WHERE `+cond+` AND i.archived_at IS NULL AND i.published_at IS NOT NULL`, arg), &i)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	setMine(&i, sid)
	if i.SourceType == "ai_blended" && !i.Mine {
		return nil, ErrNotFound
	}
	return &i, nil
}

// GetIdea, id ile tek kartı döner; kayıt yoksa ErrNotFound (bkz.
// getIdeaWhere). İç kullanım içindir (dump, pipeline) — URL yüzeyinde artık
// slug var (#110); dış istekler GetIdeaBySlug kullanır.
func (s *Store) GetIdea(ctx context.Context, id int64, sid string) (*Idea, error) {
	return s.getIdeaWhere(ctx, "i.id = $1", id, sid)
}

// GetIdeaBySlug, slug ile tek kartı döner; kayıt yoksa ErrNotFound (bkz.
// getIdeaWhere). Sayısal görünen bir değer de burada yalnız slug olarak
// aranır — id'ye asla düşülmez (#110: eski `/ideas/{id}` bağlantıları düz
// 404 alır, 301 yönlendirme yok — PO kararı).
func (s *Store) GetIdeaBySlug(ctx context.Context, slug string, sid string) (*Idea, error) {
	return s.getIdeaWhere(ctx, "i.slug = $1", slug, sid)
}

// maxIdeaSources, kart detayında listelenen kaynak satırı sayısı.
const maxIdeaSources = 10

// IdeaSources, kartın arkasındaki kaynak gönderileri döner (yeniden eskiye).
//
// İki yol vardır:
//   - Temalı kart (pain_point): ideas.source_theme_id -> theme_posts -> raw_posts.
//   - market_derived kart: temaya bağlı değildir. seeds.go tohumu raw_posts'a
//     platform='radar_seed', source_ref=<tohumun source_url'i> olarak yazar ve
//     aynı URL kartın example_quotes satırının SONUNDA durur
//     ("Kanıt (ad): kanıt — <url>"). Eşleşme bu URL üzerinden kurulur; uydurma
//     yok, birebir source_ref eşitliği aranır.
//
// Tarih: raw_posts.created_at hem NULL hem de "sıfır zaman" (0001-01-01)
// olabilir — radar_seed satırları platformda yayın zamanı taşımadığı için
// created_at'i sıfır yazılır. İki durumda da fetched_at'e düşülür.
//
// Her iki yol da tek sorgudur (N+1 yok).
func (s *Store) IdeaSources(ctx context.Context, ideaID int64) ([]IdeaSource, error) {
	themed, err := s.queryIdeaSources(ctx, `
		SELECT rp.platform, rp.community, COALESCE(rp.url, ''),
		       CASE WHEN rp.created_at IS NULL OR rp.created_at < '1970-01-01'::timestamptz
		            THEN rp.fetched_at ELSE rp.created_at END AS ts
		FROM ideas i
		JOIN theme_posts tp ON tp.theme_id = i.source_theme_id
		JOIN raw_posts rp ON rp.id = tp.post_id
		WHERE i.id = $1
		ORDER BY ts DESC, rp.id DESC
		LIMIT $2`, ideaID)
	if err != nil {
		return nil, err
	}
	if len(themed) > 0 {
		return themed, nil
	}

	// radar_seed yolu: alıntının sonundaki URL = tohumun source_ref'i.
	return s.queryIdeaSources(ctx, `
		SELECT rp.platform, rp.community, COALESCE(rp.url, ''),
		       CASE WHEN rp.created_at IS NULL OR rp.created_at < '1970-01-01'::timestamptz
		            THEN rp.fetched_at ELSE rp.created_at END AS ts
		FROM ideas i
		CROSS JOIN LATERAL unnest(i.example_quotes) AS q(quote)
		JOIN raw_posts rp
		  ON rp.platform = 'radar_seed'
		 AND rp.source_ref = substring(q.quote from '(https?://[^[:space:]]+)$')
		WHERE i.id = $1
		ORDER BY ts DESC, rp.id DESC
		LIMIT $2`, ideaID)
}

func (s *Store) queryIdeaSources(ctx context.Context, sql string, ideaID int64) ([]IdeaSource, error) {
	rows, err := s.Pool.Query(ctx, sql, ideaID, maxIdeaSources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []IdeaSource{}
	for rows.Next() {
		var src IdeaSource
		if err := rows.Scan(&src.Platform, &src.Community, &src.URL, &src.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- kart sohbeti

// ListChat, (kart, oturum) çiftinin sohbet geçmişini eskiden yeniye döner
// (en fazla `limit` mesaj — en YENİ `limit` mesaj alınır, sonra kronolojik
// sıraya çevrilir). Girişsiz kimlik: user_id yerine session_id ile filtrelenir.
func (s *Store) ListChat(ctx context.Context, ideaID int64, sid string, limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, role, message, created_at
		FROM idea_conversations
		WHERE idea_id = $1 AND session_id = $2
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, ideaID, sid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var desc []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Message, &m.CreatedAt); err != nil {
			return nil, err
		}
		desc = append(desc, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Eskiden yeniye çevir (sözleşme: GET /chat "messages" kronolojik sırada).
	out := make([]ChatMessage, len(desc))
	for i, m := range desc {
		out[len(desc)-1-i] = m
	}
	return out, nil
}

// AppendChat, (kart, oturum) sohbetine tek satır ekler ve eklenen satırı
// (id/created_at dahil) döner.
func (s *Store) AppendChat(ctx context.Context, ideaID int64, sid, role, message string) (ChatMessage, error) {
	m := ChatMessage{Role: role, Message: message}
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO idea_conversations (idea_id, session_id, role, message)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`, ideaID, sid, role, message).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return ChatMessage{}, fmt.Errorf("idea_conversations insert: %w", err)
	}
	return m, nil
}

// InsertBlendedIdea, sohbetten türeyen yeni `ai_blended` kartı yazar. Kanıt
// alanları (example_quotes/evidence_count/source_theme_id/local_evidence)
// LLM'den GELMEZ — kaynak karttan (parent) birebir kopyalanır (kart tohumu
// değişmez ilkesi, 001_init yorumu). Tek INSERT ... RETURNING zaten atomik
// (tek tx); ayrı bir Begin/Commit gerekmez.
// published_at = now() (#102): blend kartı moderasyon kuyruğuna girmez —
// zaten yalnız üreten oturuma görünüyordu (setMine/ai_blended kuralı), bu
// davranış değişmesin; ayrıca GetIdea artık published_at IS NOT NULL
// aradığından, bu satır olmadan kart oluşur oluşmaz kendi GetIdea çağrısı
// (fonksiyonun sonunda) ErrNotFound dönerdi.
func (s *Store) InsertBlendedIdea(ctx context.Context, parent *Idea, draft BlendDraft, sid string) (*Idea, error) {
	// nil slice guard: pgx nil []string'i SQL NULL yazar, NOT NULL
	// kolonları kırar (bkz. InsertPostAnalyses'teki aynı koruma).
	quotes := parent.ExampleQuotes
	if quotes == nil {
		quotes = []string{}
	}
	localEvidence := parent.LocalEvidence
	if localEvidence == nil {
		localEvidence = []string{}
	}
	tags := draft.DomainTags
	if tags == nil {
		tags = []string{}
	}

	// Slug başlıktan türetilir; UNIQUE çakışmasında yeni ek ile en fazla
	// maxSlugAttempts kez denenir (#110, InsertIdea ile aynı ilke).
	var id int64
	var err error
	for attempt := 0; attempt < maxSlugAttempts; attempt++ {
		slug := newSlug(draft.Title)
		err = s.Pool.QueryRow(ctx, `
			INSERT INTO ideas
				(title, slug, problem_statement, proposed_solution, target_user, evidence_count,
				 example_quotes, source_type, source_theme_id, local_evidence,
				 parent_idea_id, created_by_session_id,
				 urgency_score, monetization_signal, domain_tags, published_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'ai_blended', $8, $9, $10, $11, $12, $13, $14, now())
			RETURNING id`,
			draft.Title, slug, draft.ProblemStatement, draft.ProposedSolution, draft.TargetUser,
			parent.EvidenceCount, quotes, parent.SourceThemeID, localEvidence,
			parent.ID, sid, draft.UrgencyScore, draft.MonetizationSignal, tags).Scan(&id)
		if err == nil {
			return s.GetIdea(ctx, id, sid)
		}
		if !isSlugConflict(err) {
			return nil, fmt.Errorf("ai_blended ideas insert: %w", err)
		}
	}
	return nil, fmt.Errorf("ai_blended ideas insert: slug %d denemede benzersiz üretilemedi: %w", maxSlugAttempts, err)
}

// ---------------------------------------------------------------- eliminations (#138)

// InsertElimination, eliminations tablosuna tek satır yazar ve id döner.
// Çağıran (pipeline paketi) bunu BEST-EFFORT sarar: yazım hatası pipeline'ı
// durdurmaz — bu metot kendisi olağan hatayı döner, best-effort ilkesi
// burada UYGULANMAZ (çağıranın sorumluluğu).
func (s *Store) InsertElimination(ctx context.Context, e Elimination) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO eliminations (stage, subject, verdict, criterion, reason, detail)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		e.Stage, e.Subject, e.Verdict, e.Criterion, e.Reason, e.Detail).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("eliminations insert: %w", err)
	}
	return id, nil
}

// EliminationsSince, since'den (dahil) sonra oluşmuş eleme kayıtlarını en
// yeniden eskiye döner — raporlama için (#138, örn. "gün boyunca elenenler").
func (s *Store) EliminationsSince(ctx context.Context, since time.Time) ([]Elimination, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, occurred_at, stage, subject, verdict, criterion, reason, detail
		FROM eliminations
		WHERE occurred_at >= $1
		ORDER BY occurred_at DESC, id DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Elimination
	for rows.Next() {
		var e Elimination
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Stage, &e.Subject, &e.Verdict,
			&e.Criterion, &e.Reason, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EliminationCountsSince, since'den sonraki eleme sayısını stage bazında
// döner (#138: `idealode run` koşu sonu özet log satırı). Hiç kaydı olmayan
// stage haritada hiç görünmez (çağıran sıfır varsayar).
func (s *Store) EliminationCountsSince(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT stage, count(*) FROM eliminations
		WHERE occurred_at >= $1
		GROUP BY stage`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var stage string
		var n int
		if err := rows.Scan(&stage, &n); err != nil {
			return nil, err
		}
		out[stage] = n
	}
	return out, rows.Err()
}
