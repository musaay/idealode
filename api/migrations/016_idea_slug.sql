-- 016_idea_slug.sql — kart URL'lerinde sıralı id yerine slug (#110).
--
-- Herkese açık kart URL'si `/ideas/{id}` sıralı sayıydı: yetkisiz erişim
-- yoktu ama enumerasyonla toplam üretim/arşiv oranı sızıyordu ve URL ürün
-- gibi durmuyordu (PO kararı, #110). Artık `/ideas/{slug}`.
--
-- slug biçimi: slugify(title) (Türkçe transliterasyon ç/ş/ğ/ü/ö/ı, küçük
-- harf, yalnız [a-z0-9-], ilk 60 karakter, boşsa "kart") + '-' + 4 karakter
-- rastgele ek. Yeni kartlarda ek Go'da [a-z2-9] (0/o/1/l karışmasın diye
-- dar alfabe) üretilir; bu backfill'de ek `substr(md5(...),1,4)` (hex) —
-- spec'e göre SQL tarafında [a-z2-9] kısıtı şart değil, yalnız yeni
-- üretimde. Çakışmada (iki satır aynı slug'a düştüyse) ilk satır kalır,
-- geri kalanlar bir kez daha rastgele ek çeker.
--
-- Idempotent (015 kalıbı): backfill + UNIQUE kısıtı yalnız kolon İLK
-- eklendiğinde, information_schema kontrolüyle korunan TEK bir DO bloğu
-- içinde çalışır. Kolon zaten varsa (ikinci+ koşu) hiçbir şey yapılmaz —
-- mevcut slug'lar DEĞİŞMEZ (başlık sonradan değişse bile slug sabit kalır,
-- spec'in kapsam dışı bıraktığı davranış).

BEGIN;

SET search_path TO idealode, public;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'ideas' AND column_name = 'slug'
    ) THEN
        ALTER TABLE ideas ADD COLUMN slug TEXT;

        -- 1) İlk atama: TÜM kartlara (arşivli dahil) taban + rastgele ek.
        UPDATE ideas SET slug = gen.slug
        FROM (
            SELECT id,
                   coalesce(nullif(
                       btrim(
                           regexp_replace(
                               lower(translate(left(title, 60),
                                   'çÇşŞğĞüÜöÖıİ', 'ccssgguuooii')),
                               '[^a-z0-9]+', '-', 'g'),
                           '-'),
                       ''), 'kart')
                   || '-' || substr(md5(random()::text || id::text), 1, 4) AS slug
            FROM ideas
        ) AS gen
        WHERE ideas.id = gen.id;

        -- 2) Çakışma düzeltmesi (bir kez tekrar): aynı slug'a düşen
        -- satırlardan ilki (en küçük id) kalır, geri kalanlar tabanı
        -- koruyup yeni bir rastgele ek çeker. Taban, mevcut slug'ın son
        -- "-xxxx" ekinden önceki kısmıdır (adım 1'de hep bu biçimde
        -- üretildi).
        WITH dupes AS (
            SELECT id, slug,
                   row_number() OVER (PARTITION BY slug ORDER BY id) AS rn
            FROM ideas
        )
        UPDATE ideas SET slug =
            left(dupes.slug, length(dupes.slug) - 5)
            || '-' || substr(md5(random()::text || dupes.id::text || 'retry'), 1, 4)
        FROM dupes
        WHERE ideas.id = dupes.id AND dupes.rn > 1;

        ALTER TABLE ideas ALTER COLUMN slug SET NOT NULL;
        ALTER TABLE ideas ADD CONSTRAINT ideas_slug_unique UNIQUE (slug);
    END IF;
END $$;

COMMIT;
