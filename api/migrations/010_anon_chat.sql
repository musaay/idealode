-- 010_anon_chat.sql — girişsiz kart sohbeti (Idea Copilot, Faz 2 dilim 2, #66).
--
-- Kimlik giriş yerine anonim oturum çerezi (`sid`) ile kurulur. Sohbet
-- geçmişi artık user_id VEYA session_id'ye bağlı olabilir (ikisi de NULL
-- olamaz). Sohbetten türeyen fikir yeni bir `ai_blended` kart olur;
-- parent_idea_id kaynak kartı, created_by_session_id ise üreten oturumu
-- işaretler (yalnız üreten oturuma görünür — bkz. store.ListIdeasFiltered).
--
-- Idempotent: her koşuda yeniden çalışır. CHECK constraint'i pg_constraint
-- üzerinden var mı diye denetleyen bir DO bloğuyla eklenir (Postgres'te
-- ADD CONSTRAINT IF NOT EXISTS yoktur).

BEGIN;

SET search_path TO idealode, public;

-- Girişsiz sohbet: user_id artık zorunlu değil.
ALTER TABLE idea_conversations ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE idea_conversations ADD COLUMN IF NOT EXISTS session_id TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'idea_conversations_owner_check'
    ) THEN
        ALTER TABLE idea_conversations
            ADD CONSTRAINT idea_conversations_owner_check
            CHECK (user_id IS NOT NULL OR session_id IS NOT NULL);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_idea_conversations_session
    ON idea_conversations (idea_id, session_id, created_at);

-- Sohbetten türeyen kart: kaynak kart + üreten oturum.
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS parent_idea_id BIGINT
    REFERENCES ideas (id) ON DELETE SET NULL;
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS created_by_session_id TEXT;

CREATE INDEX IF NOT EXISTS idx_ideas_created_by_session
    ON ideas (created_by_session_id);

COMMIT;
