-- 008_evidence_fusion.sql — kanıt füzyonu altyapısı (#43).
--
-- market_derived kartlar gelir kanıtı taşır ama yerel talep kanıtı yoktur;
-- pipeline'daki analiz edilmiş TR/global şikayetler bu kartlara "yerel talep
-- kanıtı" olarak eşleştirilir (fuse adımı). Çift kanıtlı kart = gelir kanıtı
-- (emsal) + talep kanıtı (gerçek şikayet alıntıları).
--
-- local_evidence: kaynak linkli birebir alıntı satırları.
-- fused_at: füzyon denendi işareti (boş sonuçla da damgalanır). İlk
-- füzyondan (talep hakemi + ivme geçişi) sonra bir hafta geçince yalnız
-- ivme geçişi (github_trending eşleşmesi) yeniden dener — talep hakemi
-- tekrar çağrılmaz (#50 B parçası, bkz. IdeasNeedingFusion).

BEGIN;

SET search_path TO idealode, public;

ALTER TABLE ideas ADD COLUMN IF NOT EXISTS local_evidence TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE ideas ADD COLUMN IF NOT EXISTS fused_at TIMESTAMPTZ;

COMMIT;
