-- 015_published.sql — moderasyon kuyruğu: pipeline kartları PO onayı olmadan
-- galeride görünmesin (#102).
--
-- published_at NULL = beklemede (henüz onaylanmadı, pain_point/market_derived/
-- momentum_derived kartların InsertIdea'daki varsayılan durumu); dolu =
-- yayında. Onay şimdilik sözlü — lead DB'den elle
-- `UPDATE ideas SET published_at = now() WHERE id = ...` ile açar (ileride
-- /review sayfası ya da #17 rolüyle UI'a taşınır). ai_blended kartlar
-- InsertBlendedIdea'da published_at=now() yazar — kuyruğa hiç girmez (zaten
-- yalnız üreten oturuma görünüyorlardı, davranış değişmiyor).
--
-- Backfill: migration ÖNCESİ var olan kartlar (arşivliler dahil — arşiv
-- zaten ayrı bir filtre, published_at'ten bağımsız) geriye dönük "yayında"
-- sayılır — mevcut canlı kartlar migration sonrası kaybolmasın.
--
-- Idempotent (DÜZELTME — reviewer bulgusu): backfill yalnız kolon İLK
-- eklendiğinde, information_schema kontrolüyle korunan TEK bir DO bloğu
-- içinde çalışır. Sabit tarihe (created_at < ...) göre koşullu backfill
-- YANLIŞTI: migrate her koşuda TÜM dosyaları yeniden çalıştırdığından
-- (bkz. CLAUDE.md), sonraki her `idealode migrate` o ana kadar üretilmiş
-- ama henüz PO onayı almamış (published_at IS NULL) kartları da sessizce
-- yayınlardı. DO bloğu kolon zaten varsa (ikinci+ koşu) hiçbir şey
-- yapmaz — beklemedeki kartlar beklemede kalır.

BEGIN;

SET search_path TO idealode, public;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'ideas' AND column_name = 'published_at'
    ) THEN
        ALTER TABLE ideas ADD COLUMN published_at TIMESTAMPTZ;
        UPDATE ideas SET published_at = created_at;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_ideas_published_at ON ideas (published_at) WHERE archived_at IS NULL;

COMMIT;
