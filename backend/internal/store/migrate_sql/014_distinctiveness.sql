-- 014_distinctiveness.sql — özgünlük merceğinin ADVISORY sonucu karta yazılır (#101 v3).
--
-- Bloklamıyor: kart üretimi mercek sonucundan bağımsız her durumda devam
-- eder (pass/fail/unsure/hata). distinctiveness_verdict NULL ise mercek hiç
-- çağrılamadı (geçici hata) ya da kart bu alanları taşımayan eski bir satır
-- (ai_blended kartlarda da her zaman NULL — kaynak karttan kopyalanmaz).
-- distinctiveness_criterion, verdict="fail" ise K1-K4'ten hangisinin
-- tuttuğunu taşır (tanınmayan/uygulanamaz durumda "none"); reason kısa LLM
-- gerekçesi.
--
-- Idempotent: kolonlar IF NOT EXISTS ile eklenir, constraint her koşuda
-- düşürülüp aynı tanımla yeniden eklenir (004/013 kalıbı).

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas ADD COLUMN IF NOT EXISTS distinctiveness_verdict TEXT;
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS distinctiveness_criterion TEXT;
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS distinctiveness_reason TEXT;

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_distinctiveness_verdict_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_distinctiveness_verdict_check CHECK (
    distinctiveness_verdict IS NULL OR distinctiveness_verdict IN ('pass', 'fail', 'unsure'));

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_distinctiveness_criterion_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_distinctiveness_criterion_check CHECK (
    distinctiveness_criterion IS NULL OR distinctiveness_criterion IN ('K1', 'K2', 'K3', 'K4', 'none'));

COMMIT;
