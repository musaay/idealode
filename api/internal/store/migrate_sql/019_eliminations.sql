-- 019_eliminations.sql — elenenlerin kalıcı denetim izi (#138).
--
-- Sorun: doygunluk merceği (#101) ADVISORY'ydi — kartı bloklamıyor, yalnız
-- işaretliyordu. Üretim verisi (2026-09-14) 12 kartın 12'sinde de mercek
-- kararının PO tarafından elendiğini gösterdi: mercek doğru karar veriyor
-- ama uygulanmıyor, PO elle arşivliyor, LLM maliyeti boşa gidiyor. PO kararı:
-- K1 (doygunluk) artık BLOKLAYICI (kart DB'ye yazılmaz). Bloklamanın riski
-- "görülmeyen şey denetlenemez" — bu tabloyla kapatılıyor: her eleme noktası
-- (tutarsız tema, bloklayıcı mercek, vendor-internal skip, doygunluk bloğu)
-- kalıcı bir satır bırakır.
--
-- stage='payment_gate' şimdilik YAZILMIYOR — ileride kullanılabilir diye
-- şemada var (CHECK genişletmeden önden izin verir, savunmacı).
-- criterion yalnız stage='distinctiveness' satırlarında dolu (K1-K4); diğer
-- stage'lerde NULL — sütun o stage'lerde anlamsız.
-- reason ve detail nullable: tutarsız tema gerekçesi LLM cümlesi değil oran
-- metnidir (reason), kanıt çekilemezse detail NULL kalabilir. İkisi de
-- uygulama katmanında 500 rune'a kırpılır.
-- detail, subject tek başına yeterince açıklayıcı olmadığında (özellikle
-- tema/kova adları — "windows", "rust" gibi) PO'ya "ne elendi" bağlamını
-- taşır: kart üretildiyse problem_statement, tohumdan geldiyse seed özeti,
-- ikisi de yoksa temanın en güçlü kanıtının başlığı.
--
-- Temizlik YOK (tablo sınırsız büyür, bilinçli) — occurred_at indeksli,
-- zaman aralığı sorguları (EliminationsSince/EliminationCountsSince) bunu
-- kullanır.
--
-- Idempotent: CREATE TABLE/INDEX IF NOT EXISTS — ikinci koşuda hiçbir şey
-- değişmez (001_init.sql kalıbı).

BEGIN;

SET search_path TO idealode, public;

CREATE TABLE IF NOT EXISTS eliminations (
    id          BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    stage       TEXT NOT NULL CHECK (stage IN
                    ('incoherent_theme', 'blocking_lens', 'distinctiveness',
                     'vendor_internal', 'payment_gate')),
    subject     TEXT NOT NULL,   -- tema adı / tohum adı / üretilmiş kart başlığı
    verdict     TEXT NOT NULL,   -- şimdilik hep 'fail' (satır yalnız elemede yazılır)
    criterion   TEXT,            -- yalnız distinctiveness: K1|K2|K3|K4; diğerlerinde NULL
    reason      TEXT,            -- LLM gerekçesi ya da tutarlılık oran metni; NULL olabilir
    detail      TEXT             -- ek bağlam (problem_statement/seed özeti/en güçlü kanıt başlığı); NULL olabilir
);

CREATE INDEX IF NOT EXISTS idx_eliminations_occurred_at ON eliminations (occurred_at);

COMMIT;
