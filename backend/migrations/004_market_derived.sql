-- 004_market_derived.sql — ideas.source_type'a 'market_derived' eklenir (#28 minimal).
--
-- market_derived: pazarda GELİR/TRACTION kanıtı olan bir üründen türetilmiş
-- fikir kartı. pain_point kartlarındaki example_quotes'un muadili olarak bu
-- kartlarda example_quotes, kaynak linkli gelir/traction kanıtı satırları
-- taşır ("Kanıt (Ürün): $X MRR ... — <url>"); kanıt tahrif edilmez ilkesi
-- burada da geçerli — rakam ve link birincil kaynağa gider.
--
-- NOT (#131 hotfix — kırık migration zinciri): bu dosya eskiden burada
-- ideas_source_type_check kısıtını DROP edip 'market_derived' eklenmiş DAR
-- bir listeyle yeniden tanımlıyordu. 013_momentum_derived.sql AYNI kısıtı
-- DAHA GENİŞ bir listeyle (+ 'momentum_derived') yeniden tanımlıyor; tüm
-- migration dosyaları HER KOŞUDA yeniden çalıştığından, üretimde bir
-- momentum_derived satır oluştuğu an bu dosyanın DAR listesi o satırı ihlal
-- ediyor ve zincir 013'e hiç ulaşmadan burada (004'te) patlıyordu — prod'da
-- ilk momentum_derived kart (#123) üretilince tam olarak bu oldu. KURAL:
-- bir kısıtı yalnız EN SON tanımlayan migration tutar, daha eski dosyalar
-- aynı kısıtı yeniden EKLEMEZ (bkz. migrations/README.md). Bu dosya artık
-- ideas_source_type_check'e DOKUNMAZ — tek sahibi 013'tür.
--
-- Idempotent: bu dosyada artık şema değişikliği yok, BEGIN/COMMIT zararsız
-- no-op olarak kalır (numaralı zincirin bir sonraki dosyaya geçişini bozmaz).

BEGIN;

SET search_path TO idealode, public;

COMMIT;
