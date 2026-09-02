# IdeaLode — geliştirme rehberi

Go pipeline (ingest → analyze → synthesize → fuse): sosyal/geliştirici
platformlardan toplanan paylaşımlardan LLM ile doğrulanmış yazılım fikri
kartları üretir. Ayrıntı: README.md.

## Komutlar
- Build/test: `cd api && go build ./... && go vet ./... && go test ./...`
- Migration uygulama: `idealode migrate` (elle tetiklenir, otomatik çalışmaz)
- Pipeline: `idealode run` (= ingest + analyze + synthesize + fuse; advisory lock'lu)

## Konvansiyonlar
- Yorumlar ve log mesajları Türkçe; domain_tags/prompt'lar İngilizce.
- Migration'lar İKİ kopya tutulur: `api/migrations/` + `api/internal/store/migrate_sql/`
  ve `migrate.go`'da embed + Exec zincirine eklenir. Her migration idempotent
  olmalı (tüm dosyalar her koşuda yeniden çalıştırılır).
- nil slice tuzağı: pgx nil `[]string`'i SQL NULL yazar, `NOT NULL DEFAULT '{}'`
  kolonlarını kırar — insert sınırında guard var, koru.
- LLM cevapları savunmacı parse edilir (bitişik indeksler, boş cevap, skip).
- Connector'lar: `last_seen_ref` incremental cursor + "sakin ilerleme"
  (istek/sayfa limitleri). İlk çekim penceresi 7 gün.
- Bu repo PUBLIC: secret, iç altyapı detayı, kişisel veri commit edilmez.

## Team workflow
Esaslı işler (özellik, düzeltme, refactor) HER ZAMAN ajan takımıyla yürür,
solo değil: `.claude/agents/`dan teammate spawn edilir — `developer`
(pipeline/store) veya `ui-developer` (web katmanı) uygular (kaynağı
düzenleyen TEK rol), ardından `reviewer` doğrular; canlı doğrulama
(DB, curl, tarayıcı) lead'de —
ana oturum lead'dir: spec'i yazar (dosya seviyesinde yol haritası + kabul
kriterleri), raporları süzer, kullanıcıya tek onay özeti sunar. git/commit/
push/deploy yalnız lead'dedir. Ayrı qa rolü YOK (2026-09-02'de kaldırıldı:
reviewer + lead'in canlı kontrolü yeterli). Teammate'ler yalnız lead'le konuşur (yıldız
topolojisi). Solo çalışma yalnız şunlarda kabul: önemsiz tek satırlıklar,
salt araştırma/soru-cevap, takımın yapamayacağı infra/ops işleri (git,
deploy, canlı DB/Groq işlemleri).
