-- 007_idea_dedup.sql — idea dedup altyapısı (#14).
--
-- Etiket parçalanması (food-delivery vs delivery-app) aynı derdi iki ayrı
-- karta dönüştürebiliyor. Sentez artık yeni kart yazmadan önce pg_trgm
-- benzerliğiyle mevcut kartları tarar; mükerrer bulursa yeni kart açmak
-- yerine mevcut kartın kanıtını güçlendirir ve temayı buraya işaretler.
--
-- merged_into_idea_id: temanın kartı hangi mevcut karta katıldı. İşaretli
-- tema senteze yeniden girmez.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE themes ADD COLUMN IF NOT EXISTS merged_into_idea_id BIGINT
    REFERENCES ideas (id) ON DELETE SET NULL;

COMMIT;
