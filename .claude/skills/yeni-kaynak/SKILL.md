---
name: yeni-kaynak
description: Kullanıcı elle çağırdığında yeni bir Google Play kaynağı (uygulama) eklemek için izlenecek checklist'i verir — paket ID doğrulama, migration ekleme, migrate.go embed zinciri.
---

# Yeni Google Play kaynağı ekleme

Bu skill yalnız kullanıcı tarafından elle çağrılır (otomatik tetiklenmez).
Amaç: `sources` tablosuna yeni bir `googleplay` satırı eklemek için gereken
adımları sırayla, hatasız uygulamak.

Migration DOSYALARINI oluşturmak/düzenlemek dosya işlemidir (git değil) —
serbest. Ama migration'ı **canlı DB'ye uygulamak** (`idealode migrate`)
YASAK — bu adım lead'e bırakılır, sonda not düş.

## Adımlar

### 1) Paket ID'yi doğrula

Google Play'de gerçekten var mı, kontrol et:

```bash
curl -s -o /dev/null -w "%{http_code}\n" \
  "https://play.google.com/store/apps/details?id=<PAKET_ID>&hl=tr"
```

`200` dönmeli. `404` ise paket ID yanlış/uygulama kaldırılmış — devam etme.

### 2) Bir sonraki migration numarasını bul

```bash
ls api/migrations/ | grep -E '^[0-9]+_' | sort -V | tail -1
```

Örn. son dosya `009_fusion_sources.sql` ise yeni dosya `010_<konu>.sql` olur
(konu adı kısa ve açıklayıcı, İngilizce/TR karışık kullanım mevcut kod
tabanında serbest — mevcut isimlendirme kalıbına bak).

### 3) Yeni migration dosyasını yaz — `api/migrations/NNN_<konu>.sql`

Kalıp (mevcut `005_tr_sources.sql`, `009_fusion_sources.sql` örnek alınır):

```sql
-- NNN_<konu>.sql — <kısa gerekçe, 1-2 satır>.

BEGIN;

SET search_path TO idealode, public;

INSERT INTO sources (platform, community, category, active) VALUES
    ('googleplay', '<paket.id>', '<kategori-slug>', TRUE)
ON CONFLICT (platform, community) DO NOTHING;

COMMIT;
```

- `community` = paket ID (örn. `com.example.app`).
- `category`  = sektör slug'ı, küçük harf, tire ile (örn. `emlak`,
  `bankacilik`, `e-ticaret`, `kariyer`). Var olan bir kategoriyse aynı
  slug'ı kullan (tutarlılık); yeni sektörse kısa, açıklayıcı yeni slug seç.
- Idempotent olmalı: `ON CONFLICT (platform, community) DO NOTHING` şart.
- Dosyada secret/host adı YOK — bu repo public.

### 4) Aynı dosyayı `migrate_sql/`e kopyala

```bash
cp api/migrations/NNN_<konu>.sql \
   api/internal/store/migrate_sql/NNN_<konu>.sql
```

İki kopya birebir aynı içerikte olmalı (CLAUDE.md kuralı).

### 5) `migrate.go`'ya embed + Exec zinciri ekle

`api/internal/store/migrate.go` içinde iki yer değişir:

a) Embed bildirimi (dosyanın üstündeki `//go:embed` blokla birlikte):

```go
//go:embed migrate_sql/NNN_<konu>.sql
var migrationNNN string
```

b) `Migrate()` fonksiyonunun sonunda, sıradaki `Exec` çağrısı olarak:

```go
if _, err := conn.Exec(ctx, migrationNNN); err != nil {
    return fmt.Errorf("NNN_<konu>.sql: %w", err)
}
```

Sıra önemli — dosya numarası artan sırayla, en sona eklenir (önceki
migration'ların üstüne).

### 6) Testleri çalıştır

```bash
cd api && go build ./... && go vet ./... && go test ./...
```

### 7) NOT — canlıya uygulama

Bu skill migration DOSYASINI hazırlar; **canlı veritabanına uygulamaz**.
`idealode migrate` komutu elle, lead tarafından tetiklenir. İşin bu kısmını
"canlı doğrulama lead'e bırakıldı" diye işaretle ve dur.
