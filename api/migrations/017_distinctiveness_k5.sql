-- 017_distinctiveness_k5.sql — özgünlük merceğine K5 "heyecan" kriteri (#114).
--
-- K1-K4'e ek olarak K5: fikir mantıklı ve para kazandırabilir ama mekaniği
-- bilinen bir ürünün başka pazara/dile/nişe taşınmış hali — yeni bir mekanik,
-- dağıtım/fiyatlama modeli ya da beklenmedik alan kombinasyonu yok. Diğer
-- kriterler gibi ADVISORY: bloklamaz, yalnız işaretler.
--
-- Idempotent (013/014 kalıbı): constraint her koşuda düşürülüp K5'i içeren
-- aynı tanımla yeniden eklenir. Mevcut kartların criterion değerleri (K1-K4,
-- none, NULL) bu ADD ile bozulmaz — hepsi yeni listenin bir alt kümesi.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_distinctiveness_criterion_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_distinctiveness_criterion_check CHECK (
    distinctiveness_criterion IS NULL OR distinctiveness_criterion IN ('K1', 'K2', 'K3', 'K4', 'K5', 'none'));

COMMIT;
