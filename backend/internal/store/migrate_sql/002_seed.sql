-- 002_seed.sql — roller + seed kaynak listesi (Rev 2, PO önerisi).
-- Idempotent: ON CONFLICT DO NOTHING.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO roles (name) VALUES ('admin'), ('member')
ON CONFLICT (name) DO NOTHING;

-- Stack Exchange: geniş tarama YOK — seçili siteler (Faz 0).
-- softwarerecs: çekirdek kaynak — sitenin tamamı "şu işi yapan araç var mı?"
INSERT INTO sources (platform, community, category, active) VALUES
    ('stackexchange', 'softwarerecs', 'core',      TRUE),
    ('stackexchange', 'webapps',      'secondary', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

-- Hacker News: community = Algolia arama sorgusu (Faz 0).
INSERT INTO sources (platform, community, category, active) VALUES
    ('hackernews', 'is there a tool',    'search', TRUE),
    ('hackernews', 'I wish there was',   'search', TRUE),
    ('hackernews', 'why is there no',    'search', TRUE),
    ('hackernews', 'looking for an app', 'search', TRUE),
    ('hackernews', 'any alternative to', 'search', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

-- GitHub Issues: community = search query, advanced_search=true ile (Faz 1'de
-- connector gelir; o zamana dek ingest bu platformu "connector yok" diye atlar).
-- Gürültüye göre iterasyon beklenir.
INSERT INTO sources (platform, community, category, active) VALUES
    ('github', 'label:"feature request" state:open', 'search', TRUE),
    ('github', '"i wish" in:body is:issue',          'search', TRUE),
    ('github', '"is there a way to" is:issue',       'search', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

-- Product Hunt: community = topic slug (Faz 1).
INSERT INTO sources (platform, community, category, active) VALUES
    ('producthunt', 'developer-tools',         'topic', TRUE),
    ('producthunt', 'saas',                    'topic', TRUE),
    ('producthunt', 'productivity',            'topic', TRUE),
    ('producthunt', 'artificial-intelligence', 'topic', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

-- Reddit: wired ama UYKUDA (OAuth onayı yok) — seed'ler active=FALSE bekler;
-- erişim açılırsa yalnız env doldurulup bu satırlar aktive edilir.
INSERT INTO sources (platform, community, category, active) VALUES
    ('reddit', 'SomebodyMakeThis', 'subreddit', FALSE),
    ('reddit', 'AppIdeas',         'subreddit', FALSE),
    ('reddit', 'SaaS',             'subreddit', FALSE),
    ('reddit', 'SideProject',      'subreddit', FALSE),
    ('reddit', 'smallbusiness',    'subreddit', FALSE),
    ('reddit', 'productivity',     'subreddit', FALSE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
