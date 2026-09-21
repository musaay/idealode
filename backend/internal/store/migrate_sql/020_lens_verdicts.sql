-- 020_lens_verdicts.sql — mercek kararları kalıcı olsun (#164, üst plan #163 §3.1/§6.1).
--
-- Sorun: mercek adı ve pass/unsure gerekçeleri hiçbir yerde DB'ye
-- yazılmıyordu — denetlenebilirlik sıfırdı (#163 §1: "15 elemenin 15'i log
-- kazısıyla atfedildi"). Bu migration İKİ tabloya ekleme yapar:
--
-- ideas.lens_verdicts jsonb: kartı üreten/etkileyen HER mercek çağrısının
--   (üçüncü-taraf, veri-erişimi, pazar-işlerliği, özgünlük, ivme
--   tohumlarında +ürünleştirilebilirlik) kaydı — dizi, her eleman
--   {lens, prompt_version, subject: "card"|"seed", verdict: pass|fail|
--   unsure|error, reason, at}. Organik yolda TÜMÜ subject="card" (mercekler
--   zaten üretilmiş kart alanları üzerinde çalışır); tohum yolunda 3(-4)
--   bloklayıcı mercek subject="seed" (ham tohum alanları üzerinde çalışır),
--   özgünlük subject="card" (kart üretildikten SONRA kart alanları
--   üzerinde çalışır).
--
-- eliminations.check text: elemeyi yapan merceğin adı (stage=blocking_lens/
--   distinctiveness satırlarında dolu; incoherent_theme/vendor_internal gibi
--   mercek-dışı elemelerde NULL — bu satırların "check"i yok). "check"
--   Postgres'te ayrılmış anahtar sözcük — kolon adı DAİMA çift tırnaklı
--   kullanılmalı (bkz. internal/store/queries.go).
--
-- eliminations.verdicts jsonb: kart hiç yazılmadan elenen durumda (K1
--   doygunluk ya da bloklayıcı mercek fail'i) o ana kadar yapılan TÜM mercek
--   çağrılarının (pass dahil) kaydı — ideas.lens_verdicts'in ŞEMASIYLA AYNI;
--   kart yazılmadığından tek kalıcı yeri burasıdır. Kart yazılıp K2-K4 gibi
--   bloklamayan bir kriter kaydedildiğinde de aynı liste burada tekrar durur
--   (zararsız, denetlenebilirlik lehine).
--
-- Çağrı HATASI (429/413/400/ağ) verdict="error" + hata metniyle (500 rune'a
-- kırpılmış) yazılır — NULL (hiç çağrılmadı) ile karışmasın diye. "unsure"
-- hâlâ bloklamaz (PO kararı #131, değişmiyor) — bu migration yalnız KAYIT
-- ekliyor, mevcut davranış (hata halinde kart organik yolda yine yazılır,
-- tohum yolunda mercek hatasında tohum atlanır/yeniden denenir) DEĞİŞMİYOR.
--
-- nil-slice tuzağı (CLAUDE.md): uygulama katmanı nil diziyi jsonb'ye
-- yazmadan önce boş diziye ([]store.LensVerdict{}) indirger — pgx'in jsonb
-- encode'u (JSONCodec, encoding/json fallback) nil slice'ı json "null"
-- değerine çevirir (SQL NULL değil, ama istenen "[]" de değil); guard bunu
-- önler, tıpkı text[] kolonlarındaki nil-slice korumasıyla aynı ilkeyle.
--
-- Idempotent: kolonlar IF NOT EXISTS ile eklenir (018/019 kalıbı) — ikinci
-- koşuda hiçbir şey değişmez.

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas ADD COLUMN IF NOT EXISTS lens_verdicts JSONB NOT NULL DEFAULT '[]';

ALTER TABLE eliminations ADD COLUMN IF NOT EXISTS "check" TEXT;
ALTER TABLE eliminations ADD COLUMN IF NOT EXISTS verdicts JSONB NOT NULL DEFAULT '[]';

COMMIT;
