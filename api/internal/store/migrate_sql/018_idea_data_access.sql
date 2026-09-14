-- 018_idea_data_access.sql — veri-erişimi merceğinin kararı karta yazılır (#131).
--
-- distinctiveness_* deseninin (014) BİREBİR aynısı — TEK farkla: bu mercek
-- BLOCKING'tir (distinctiveness ADVISORY'dir), bu yüzden data_access_verdict
-- kartta ASLA "fail" görülmez (fail verdict kartın üretilmesini zaten
-- engeller, bkz. pipeline.blockedByIdeaLens) — yine de constraint diğer iki
-- merceğinki gibi üç değeri de kabul eder (savunmacı: ileride biri
-- BLOCKING/ADVISORY ayrımını değiştirirse şema önden kısıtlamasın).
-- data_access_verdict NULL ise mercek hiç çağrılamadı (geçici hata) ya da
-- kart bu alanları taşımayan bir tür (seed-türetilmiş market_derived/
-- momentum_derived + ai_blended — hep NULL, yalnız synthesize.go'nun organik
-- (pain_point) yolu bu alanları doldurur).
--
-- Idempotent: kolonlar IF NOT EXISTS ile eklenir, constraint her koşuda
-- düşürülüp aynı tanımla yeniden eklenir (004/013/014 kalıbı).

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas ADD COLUMN IF NOT EXISTS data_access_verdict TEXT;
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS data_access_reason TEXT;

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_data_access_verdict_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_data_access_verdict_check CHECK (
    data_access_verdict IS NULL OR data_access_verdict IN ('pass', 'fail', 'unsure'));

COMMIT;
