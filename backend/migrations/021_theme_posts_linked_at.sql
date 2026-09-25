-- 021_theme_posts_linked_at.sql — retheme sonsuz döngü kırılması (#189, üst plan #136).
--
-- Sorun: Retheme, eski tip temalardaki (theme_name = domain_tag ya da
-- domain_tag NULL, frekans eşik üstü, kartı olmayan) theme_posts bağlarını
-- çözer; bir sonraki GroupThemes bu gönderileri LLM ile yeniden kümeler.
-- Model emin olmadığı bir gönderiyi ATLARSA, gönderi ESKİ davranışa
-- (domain_tag'i doğrudan tema adı sayma) düşer ve LinkThemePost ile AYNI
-- eski etiket temasına YENİ bir bağ olarak geri girer — bu bağ, eski
-- bağdan hiçbir şekilde ayırt edilemediği için yine retheme hedefi olur,
-- kuyruk asla sıfıra inmez.
--
-- Çözüm: theme_posts.linked_at, bağın oluşturulma zamanını tutar. Bu
-- migration'dan ÖNCEKİ satırlar NULL kalır (= eski bağ, retheme hedefi);
-- bu migration'dan SONRA LinkThemePost ile eklenen HER bağ (gerçek LLM
-- kümelemesi ya da eski davranışa düşüş fark etmeksizin) now() alır (=
-- GroupThemes'ten geçti, retheme hedefi DEĞİL). Böylece bir gönderi eski
-- davranışa düşüp aynı temaya geri bağlansa bile artık YENİ bir linked_at
-- taşır ve döngü kırılır.
--
-- KRİTİK: kolon eklemesi ve DEFAULT ataması İKİ AYRI ifade olmak ZORUNDA —
-- tek ifadede "ADD COLUMN ... DEFAULT now()" yazılırsa Postgres mevcut TÜM
-- satırları o an ki now() ile doldurur, tüm eski-bağ backlog'u (retheme'in
-- asıl hedef kümesi) tek seferde kaybolur. Önce DEFAULT'suz eklenir (yeni
-- kolon mevcut satırlarda NULL kalır), sonra DEFAULT ayrı ifadeyle
-- eklenir (yalnız BUNDAN SONRAKİ insert'leri etkiler, mevcut satırlara
-- dokunmaz).
--
-- Idempotent: ADD COLUMN IF NOT EXISTS + SET DEFAULT (DEFAULT ataması zaten
-- idempotent, ikinci koşuda aynı değeri yeniden yazar) — ikinci koşuda
-- mevcut satırların linked_at'i DEĞİŞMEZ.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE theme_posts ADD COLUMN IF NOT EXISTS linked_at timestamptz;
ALTER TABLE theme_posts ALTER COLUMN linked_at SET DEFAULT now();

COMMIT;
