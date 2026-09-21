-- 005_tr_sources.sql — TR pazarı kaynakları (Faz 1 rescope).
-- Kalite kapısı geri bildirimi: kartlar TR pazarından gelmeli. Sektör
-- uygulamalarının Google Play yorumları + Technopat forumu eklenir.
--
-- googleplay: community = uygulama paket kimliği, category = sektör slug'ı.
-- technopat:  community = forum slug'ı ('-' = tüm forumların akışı).
-- Idempotent: ON CONFLICT DO NOTHING.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO sources (platform, community, category, active) VALUES
    ('googleplay', 'com.sahibinden',                        'emlak',      TRUE),
    ('googleplay', 'com.amvg.hemlak',                       'emlak',      TRUE),
    ('googleplay', 'com.emlakjet.kurumsal.sekizbit',        'emlak',      TRUE),
    ('googleplay', 'com.garanti.cepsubesi',                 'bankacilik', TRUE),
    ('googleplay', 'com.pozitron.iscep',                    'bankacilik', TRUE),
    ('googleplay', 'com.akbank.android.apps.akbank_direkt', 'bankacilik', TRUE),
    ('googleplay', 'trendyol.com',                          'e-ticaret',  TRUE),
    ('googleplay', 'com.pozitron.hepsiburada',              'e-ticaret',  TRUE),
    ('googleplay', 'com.getir',                             'e-ticaret',  TRUE),
    ('googleplay', 'tr.gov.turkiye.edevlet.kapisi',         'kamu',       TRUE),
    ('technopat',  '-',                                     'forum',      TRUE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
