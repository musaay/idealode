-- 006_theme_coherence.sql — tema tutarlılık kontrolü (sentez kalitesi).
--
-- Sorun: tema anahtarı tek tag olduğundan eşik "dert frekansı" değil "tag
-- frekansı" sayıyordu; aynı tag altında 3 FARKLI dert de kart tetikliyordu.
-- Sentezden önce LLM kova içi tutarlılığı denetler; tutarlı alt küme eşiği
-- geçemezse tema incoherent_at ile işaretlenir. Temaya YENİ kanıt gelince
-- (last_seen > incoherent_at) tema yeniden değerlendirmeye girer.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE themes ADD COLUMN IF NOT EXISTS incoherent_at TIMESTAMPTZ;

COMMIT;
