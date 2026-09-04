-- 013_momentum_derived.sql — ideas.source_type CHECK'e 'momentum_derived'
-- eklenir (#89, #50 B'ye bağımlı).
--
-- momentum_derived: GitHub Trending'de kalıcılık gösteren (14 günde ≥3 gün)
-- ve sıkı bir kapıdan (yaş, kullanım, ürünleştirilebilirlik, 3 mevcut
-- mercek) geçen bir repodan türetilmiş fikir kartı. GELİR KANITI YOKTUR —
-- example_quotes koddan hesaplanan "★N toplam, +M/gün ... — gelir kanıtı
-- yok" satırını taşır, market_derived'in gelir-kanıtlı satırıyla
-- KARIŞTIRILMAZ.
--
-- Idempotent: constraint her koşuda düşürülüp aynı tanımla yeniden eklenir
-- (004_market_derived.sql kalıbı).

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas DROP CONSTRAINT IF EXISTS ideas_source_type_check;
ALTER TABLE ideas ADD CONSTRAINT ideas_source_type_check CHECK (source_type IN
    ('pain_point', 'ai_generated', 'ai_blended', 'market_derived', 'user_created', 'momentum_derived'));

COMMIT;
