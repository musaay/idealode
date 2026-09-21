-- 012_github_trending.sql — GitHub trending kaynağı (#50 B parçası).
--
-- github.com/trending?since=daily'den günlük yükselen repoları çeken
-- github_trending connector'ı için tek kaynak satırı. Dil filtresi yok
-- (issue kararı); community='daily' sabit.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO sources (platform, community, category, active) VALUES
    ('github_trending', 'daily', 'trending', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
