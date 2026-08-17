-- 004_market_derived.sql — ideas.source_type'a 'market_derived' eklenir (#28 minimal).
--
-- market_derived: pazarda GELİR/TRACTION kanıtı olan bir üründen türetilmiş
-- fikir kartı. pain_point kartlarındaki example_quotes'un muadili olarak bu
-- kartlarda example_quotes, kaynak linkli gelir/traction kanıtı satırları
-- taşır ("Kanıt (Ürün): $X MRR ... — <url>"); kanıt tahrif edilmez ilkesi
-- burada da geçerli — rakam ve link birincil kaynağa gider.
--
-- Idempotent: constraint her koşuda düşürülüp aynı tanımla yeniden eklenir.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_source_type_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_source_type_check CHECK (source_type IN
    ('pain_point', 'ai_generated', 'ai_blended', 'market_derived', 'user_created'));

COMMIT;
