-- 003_more_hn_sources.sql — HN sorgu setini genişletme.
-- Mevcut 5 sorgu haftada ~25 post üretiyor ve temalar birleşemiyor;
-- hacmi artırmak için ek arama kalıpları. Idempotent: ON CONFLICT DO NOTHING.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO sources (platform, community, category, active) VALUES
    ('hackernews', 'is there an app',         'search', TRUE),
    ('hackernews', 'is there a service',      'search', TRUE),
    ('hackernews', 'is there any tool',       'search', TRUE),
    ('hackernews', 'looking for a tool',      'search', TRUE),
    ('hackernews', 'i would pay for',         'search', TRUE),
    ('hackernews', 'why isn''t there',        'search', TRUE),
    ('hackernews', 'wish there was a',        'search', TRUE),
    ('hackernews', 'does anyone know a tool', 'search', TRUE),
    ('hackernews', 'recommend a tool',        'search', TRUE),
    ('hackernews', 'no good tool for',        'search', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
