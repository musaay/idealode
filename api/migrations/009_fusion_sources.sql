-- 009_fusion_sources.sql — füzyon boşluk analizi kaynakları.
--
-- İlk füzyon koşusu (2026-08-21) kullanıcının onayladığı 5 market_derived
-- kartın alanlarının (KOBİ satışı, konuşma terapisi, kariyer, sosyal satış)
-- mevcut kaynaklarca hiç kapsanmadığını gösterdi. Bu sektör uygulamaları
-- eklenir; birikecek yorumlar füzyonla kartlara yerel talep kanıtı sağlar.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO sources (platform, community, category, active) VALUES
    ('googleplay', 'com.whatsapp.w4b',          'kobi-satis', TRUE),
    ('googleplay', 'com.otsimo.app',            'ozel-egitim', TRUE),
    ('googleplay', 'com.kariyer.androidproject','kariyer',    TRUE),
    ('googleplay', 'com.linkedin.android',      'kariyer',    TRUE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
