---
name: durum
description: Günlük durum raporu — kullanıcı elle çağırdığında kart/tema/analiz sayılarını (DB) ve açık issue listesini (gh) yarım sayfa sade Türkçe özetler.
---

# Günlük durum raporu

Bu skill yalnız kullanıcı tarafından elle çağrılır. Amaç: pipeline'ın
güncel durumunu (kart sayıları, bekleyen iş, açık issue'lar) YARIM SAYFA
sade Türkçe özetle raporlamak — detaya boğma (bkz. reports-keep-simple).

Canlı DB'ye salt-okunur (SELECT) sorgu atar; hiçbir yazma/migration işlemi
yapmaz.

## 1) Bağlantı bilgisini yükle

DATABASE_URL'i repo kökündeki `.env`'den al, terminale/rapora asla yazma:

```bash
set -a; source .env; set +a
```

## 2) DB sorguları

Şema `idealode` (tablolar: `ideas`, `raw_posts`, `post_analysis`, `themes`).

**Toplam kart, source_type kırılımlı:**

```bash
psql "$DATABASE_URL" -c "
SELECT source_type, count(*) AS adet
FROM idealode.ideas
GROUP BY source_type
ORDER BY source_type;"
```

**Son 24 saatte yeni kart:**

```bash
psql "$DATABASE_URL" -c "
SELECT count(*) AS son_24_saat
FROM idealode.ideas
WHERE created_at > now() - interval '24 hours';"
```

**Bekleyen analiz sayısı** (henüz `post_analysis` satırı olmayan `raw_posts`):

```bash
psql "$DATABASE_URL" -c "
SELECT count(*) AS bekleyen_analiz
FROM idealode.raw_posts rp
LEFT JOIN idealode.post_analysis pa ON pa.post_id = rp.id
WHERE pa.id IS NULL;"
```

**Senteze hazır tema sayısı** (bkz. `store.ThemesReadyForSynthesis` —
frekans eşiği varsayılan 3 [`MIN_THEME_EVIDENCE`], henüz kart üretilmemiş,
başka karta katılmamış, tutarsız işaretlenmemiş):

```bash
psql "$DATABASE_URL" -c "
SELECT count(*) AS senteze_hazir_tema
FROM idealode.themes t
WHERE t.frequency >= 3
  AND NOT EXISTS (SELECT 1 FROM idealode.ideas i WHERE i.source_theme_id = t.id)
  AND t.merged_into_idea_id IS NULL
  AND (t.incoherent_at IS NULL OR t.last_seen > t.incoherent_at);"
```

## 3) Açık issue'lar

```bash
gh issue list --repo musaay/idealode --state open --limit 30
```

## 4) Raporla

Yukarıdaki 4 DB sorgusu + issue listesini YARIM SAYFA, sade Türkçe rapora
dönüştür:

- Toplam kart (source_type kırılımı tek satırda, örn. "pain_point: 12,
  ai_generated: 3, market_derived: 5")
- Son 24 saatte kaç yeni kart geldi
- Bekleyen analiz sayısı (yüksekse pipeline gecikmiş olabilir, tek cümle not)
- Senteze hazır tema sayısı
- Açık issue sayısı + varsa dikkat çeken 2-3 tanesi (başlık + numara)

Ham sorgu çıktısını (tablo/satır dökümü) rapora YAPIŞTIRMA — sadece
sayıları ve kısa yorumu yaz. Rapor formatı için `reports-keep-simple`
kuralını uygula.
