-- 011_archive_ideas.sql — kart arşivleme (#74).
--
-- Silmek yerine geri alınabilir arşivleme: `archived_at` doluysa kart
-- galeri listesinde ve detay sorgusunda görünmez (bkz. store.ListIdeasFiltered,
-- store.GetIdea). CLI `dump` gibi araçlar (store.ListIdeas) etkilenmez.
-- Arşivleme bu turda yalnız elle SQL UPDATE ile yapılır — UI/endpoint yok.
--
-- Idempotent: her koşuda yeniden çalışır.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

COMMIT;
