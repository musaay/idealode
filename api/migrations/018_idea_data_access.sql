-- 018_idea_data_access.sql — veri-erişimi merceğinin kararı karta yazılır (#131).
--
-- distinctiveness_* deseninin (014) BİREBİR aynısı — TEK farkla: bu mercek
-- BLOCKING'tir (distinctiveness ADVISORY'dir), bu yüzden data_access_verdict
-- kartta ASLA "fail" görülmez (fail verdict kartın üretilmesini zaten
-- engeller, hem organik yolda pipeline.blockedByIdeaLens hem tohum yolunda
-- seeds.ProcessSeeds'in hasFail dalı) — yine de constraint diğer iki
-- merceğinki gibi üç değeri de kabul eder (savunmacı: ileride biri
-- BLOCKING/ADVISORY ayrımını değiştirirse şema önden kısıtlamasın).
-- "unsure" HİÇBİR yolda kartı bloklamaz (#131 PO düzeltmesi: sıcaklık 0
-- olduğundan unsure deterministiktir — tohum yolunda da organik yoldaki
-- gibi karta yazılır, tohumu bir daha denenmek üzere beklemede bırakmaz).
-- data_access_verdict NULL ise mercek hiç çağrılamadı (geçici hata) ya da
-- kart bu alanları taşımayan bir tür (yalnız ai_blended — kaynak karttan
-- kopyalanmaz, hep NULL); hem organik (pain_point) hem tohum
-- (market_derived/momentum_derived) yolu bu alanları doldurur.
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
