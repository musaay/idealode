-- 017_theme_domain_tag.sql — temaların hangi kovadan (domain_tag) doğduğunu
-- kaydeder (#127).
--
-- Sorun: GroupThemes bir gönderiyi yalnız birincil domain_tag'ine göre
-- temaya bağlıyordu — "machine-learning", "mobile-banking" gibi geniş
-- kategoriler doğrudan tema oluyor, aynı etiket altındaki FARKLI dertler
-- tek temada birikiyordu (üretimde ölçüldü: 10 temanın 10'u sentezin
-- tutarlılık merceğinden düştü). Artık kova içi kümeleme LLM ile yapılıyor
-- (GroupThemes) — bu kolon LLM'e "bu kovada daha önce açılmış temalar"
-- bağlamını vermek için gerekli (ThemesByDomainTag).
--
-- Idempotent (006 kalıbı): ADD COLUMN IF NOT EXISTS + yalnız domain_tag
-- boş olan satırlara uygulanan UPDATE — ikinci koşuda kolon zaten var ve
-- tüm satırlar dolu olduğundan hiçbir şey değişmez.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE themes ADD COLUMN IF NOT EXISTS domain_tag TEXT;

-- Mevcut temalar zaten tag adını taşıyordu (eski davranış: tema anahtarı =
-- domain_tag) — geriye dönük yeniden kümeleme YOK (kapsam dışı, #127),
-- yalnız bu yeni kolon dolduruluyor.
UPDATE themes SET domain_tag = theme_name WHERE domain_tag IS NULL;

CREATE INDEX IF NOT EXISTS idx_themes_domain_tag ON themes (domain_tag);

COMMIT;
