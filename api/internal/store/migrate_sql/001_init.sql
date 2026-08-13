-- 001_init.sql — IdeaLode nihai şeması.
--
-- Şema BAŞTAN nihai hâliyle kurulur (users/roles/idea_user_status dahil);
-- Faz 0 bu tabloların bir kısmını boş bırakır — sonradan migration çilesi
-- yaşanmaz (Rev 2 kararı).
--
-- İzolasyon: tablo-adı prefix'i DEĞİL, gerçek Postgres şeması `idealode`.
-- Bağlantıda search_path=idealode,public set edilir (bkz. internal/store).
-- cv-search'ün Railway Postgres instance'ı reuse edilir; pool max_conns <= 5.

BEGIN;

CREATE SCHEMA IF NOT EXISTS idealode;

-- pg_trgm: idea dedup için (similarity()); extension objeleri public'te yaşar.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

SET search_path TO idealode, public;

-- --------------------------------------------------------------- kullanıcılar
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    google_sub    TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL,
    name          TEXT,
    avatar_url    TEXT,
    -- "aktif kullanıcı" tanımı (generate job'ı): son 14 günde girişi olan
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id BIGINT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

-- ------------------------------------------------------------------ kaynaklar
-- DB-backed, paylaşılan kaynak listesi; config dosyası YOK.
-- `community` = connector'a özgü selector: Reddit -> subreddit; SE -> site
-- slug; HN -> kayıtlı arama sorgusu; GitHub -> search query; PH -> topic slug.
CREATE TABLE IF NOT EXISTS sources (
    id            BIGSERIAL PRIMARY KEY,
    platform      TEXT NOT NULL,
    community     TEXT NOT NULL,
    category      TEXT,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    last_seen_ref TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (platform, community)
);

-- ------------------------------------------------------------------ ham veri
CREATE TABLE IF NOT EXISTS raw_posts (
    id         BIGSERIAL PRIMARY KEY,
    platform   TEXT NOT NULL,
    source_ref TEXT NOT NULL,   -- platformun kendi kimliği (HN objectID, SE question_id, ...)
    community  TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL DEFAULT '',
    author     TEXT,
    url        TEXT,
    score      INTEGER,
    created_at TIMESTAMPTZ,               -- platformdaki yayınlanma zamanı
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (platform, source_ref)
);

CREATE INDEX IF NOT EXISTS idx_raw_posts_fetched_at ON raw_posts (fetched_at);

-- ------------------------------------------------------------- classification
CREATE TABLE IF NOT EXISTS post_analysis (
    id                 BIGSERIAL PRIMARY KEY,
    post_id            BIGINT NOT NULL UNIQUE REFERENCES raw_posts (id) ON DELETE CASCADE,
    classification     TEXT NOT NULL CHECK (classification IN
                           ('pain_point', 'feature_request', 'complaint', 'noise')),
    problem_summary    TEXT,       -- OUTPUT_LANG (TR)
    target_audience    TEXT,       -- OUTPUT_LANG (TR)
    domain_tags        TEXT[] NOT NULL DEFAULT '{}',  -- kanonik EN slug'lar
    willingness_to_pay BOOLEAN NOT NULL DEFAULT FALSE,
    -- TRUE ise LLM'e hiç gitmedi: keyword ön-filtresi noise saydı.
    -- LLM'in noise kararından ayırt edilir (eşik iterasyonu görünürlüğü).
    prefiltered        BOOLEAN NOT NULL DEFAULT FALSE,
    analyzed_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_post_analysis_classification ON post_analysis (classification);

-- ------------------------------------------------------------------- temalar
CREATE TABLE IF NOT EXISTS themes (
    id          BIGSERIAL PRIMARY KEY,
    theme_name  TEXT NOT NULL UNIQUE,   -- kanonik EN slug (gruplama anahtarı)
    description TEXT,                   -- OUTPUT_LANG (TR)
    frequency   INTEGER NOT NULL DEFAULT 0,
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Array kolonu YOK (Rev 2): tema-post ilişkisi join tablosunda; evidence
-- endpoint'i buradan beslenir.
CREATE TABLE IF NOT EXISTS theme_posts (
    theme_id BIGINT NOT NULL REFERENCES themes (id) ON DELETE CASCADE,
    post_id  BIGINT NOT NULL REFERENCES raw_posts (id) ON DELETE CASCADE,
    PRIMARY KEY (theme_id, post_id)
);

CREATE INDEX IF NOT EXISTS idx_theme_posts_post ON theme_posts (post_id);

-- -------------------------------------------------------------------- ideas
-- Paylaşılan "tohum" — refinement chat ile ASLA değişmez; kullanıcıya özel
-- her şey idea_user_status'ta. `status` ve `build_prompt` kolonları bilinçli
-- olarak YOK.
CREATE TABLE IF NOT EXISTS ideas (
    id                         BIGSERIAL PRIMARY KEY,
    title                      TEXT NOT NULL,             -- TR
    problem_statement          TEXT NOT NULL DEFAULT '',  -- TR
    proposed_solution          TEXT NOT NULL DEFAULT '',  -- TR
    target_user                TEXT NOT NULL DEFAULT '',  -- TR
    evidence_count             INTEGER NOT NULL DEFAULT 0,
    example_quotes             TEXT[] NOT NULL DEFAULT '{}',  -- orijinal dil (EN) — kanıt tahrif edilmez
    source_type                TEXT NOT NULL CHECK (source_type IN
                                   ('pain_point', 'ai_generated', 'ai_blended', 'user_created')),
    -- pain_point idea'larında kaynak tema (evidence: theme_posts üzerinden)
    source_theme_id            BIGINT REFERENCES themes (id) ON DELETE SET NULL,
    created_by_user_id         BIGINT REFERENCES users (id) ON DELETE SET NULL,
    urgency_score              INTEGER CHECK (urgency_score BETWEEN 1 AND 5),
    monetization_signal        INTEGER CHECK (monetization_signal BETWEEN 0 AND 5), -- 0 = sinyal yok
    known_competitors_ai_guess TEXT,  -- "AI tahmini, doğrulanmamış" — FİLTRE DEĞİL, bilgi notu
    domain_tags                TEXT[] NOT NULL DEFAULT '{}',  -- kanonik EN slug'lar
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- pg_trgm dedup için (Faz 1'de similarity() sorguları bu indeksleri kullanır)
CREATE INDEX IF NOT EXISTS idx_ideas_title_trgm
    ON ideas USING gin (title gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_ideas_problem_trgm
    ON ideas USING gin (problem_statement gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_ideas_source_theme ON ideas (source_theme_id);

-- ------------------------------------------------- kullanıcıya özel katman
-- `new` sanal durumdur: satır yokluğu = new (API LEFT JOIN + COALESCE ile
-- döner). Satır varken status NULL'a çekilebilir (= new'e geri çekme).
CREATE TABLE IF NOT EXISTS idea_user_status (
    user_id                    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    idea_id                    BIGINT NOT NULL REFERENCES ideas (id) ON DELETE CASCADE,
    status                     TEXT CHECK (status IN
                                   ('reviewed', 'favorited', 'rejected', 'planned')),
    refined_problem_statement  TEXT,
    refined_proposed_solution  TEXT,
    refined_target_user        TEXT,
    refined_build_prompt       TEXT,      -- EN üretilir (PO kararı)
    refined_domain_tags        TEXT[],
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, idea_id)
);

-- `role` = mesaj yazarı (user|assistant); madde 1'deki yetki rolüyle karışmaz.
CREATE TABLE IF NOT EXISTS idea_conversations (
    id         BIGSERIAL PRIMARY KEY,
    idea_id    BIGINT NOT NULL REFERENCES ideas (id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    message    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_idea_conversations_lookup
    ON idea_conversations (idea_id, user_id, created_at);

CREATE TABLE IF NOT EXISTS idea_comments (
    id         BIGSERIAL PRIMARY KEY,
    idea_id    BIGINT NOT NULL REFERENCES ideas (id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    text       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_idea_comments_lookup ON idea_comments (idea_id, user_id);

-- ------------------------------------------------------------------ pipeline
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id                   BIGSERIAL PRIMARY KEY,
    job_type             TEXT NOT NULL,
    -- yalnız generate/manuel tetiklemede dolu
    triggered_by_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at          TIMESTAMPTZ,
    status               TEXT NOT NULL DEFAULT 'running'
                             CHECK (status IN ('running', 'success', 'failed')),
    items_processed      INTEGER NOT NULL DEFAULT 0,
    error_message        TEXT
);

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_job ON pipeline_runs (job_type, started_at DESC);

COMMIT;
