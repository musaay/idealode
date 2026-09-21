# Migration'lar

Basit, sıra numaralı `.sql` dosyaları; migration aracı yok. Dosyalar
idempotent yazılmıştır (`IF NOT EXISTS` / `ON CONFLICT DO NOTHING`), tekrar
çalıştırmak güvenlidir.

Önerilen yol, binary'ye embed edilmiş dosyaları uygulayan CLI komutu:

```sh
./idealode migrate
```

Alternatif olarak `psql` ile elle:

```sh
psql "$DATABASE_URL" -f backend/migrations/001_init.sql
psql "$DATABASE_URL" -f backend/migrations/002_seed.sql
```

Notlar:

- Tüm nesneler `idealode` Postgres şemasında yaşar; uygulama bağlantısı
  `search_path=idealode,public` set eder.
- `pg_trgm` extension'ı gerekir — 001 içinde `CREATE EXTENSION IF NOT
  EXISTS` var.
- **Kısıt sahipliği (#131 hotfix):** bir CHECK/UNIQUE kısıtını yalnız EN SON
  tanımlayan migration tutar; daha eski bir dosya aynı kısıtı (aynı isimle
  DROP+ADD ederek) yeniden EKLEMEZ. Tüm dosyalar her koşuda yeniden
  çalıştığından, eski dosya dar bir tanımı yeniden uygularsa ve yeni
  değeri kullanan bir satır DB'de zaten varsa, o eski dosyanın ADD
  CONSTRAINT'i o satırı ihlal eder ve zincir orada durur — daha SONRAKİ
  (genişleten) migration'a hiç ulaşılmaz. Örnek: 004_market_derived.sql
  `ideas_source_type_check`'i dar bir listeyle yeniden tanımlıyordu,
  013_momentum_derived.sql aynı kısıtı geniş listeyle yeniden tanımlıyordu;
  ilk `momentum_derived` satırı üretimde oluşunca zincir 004'te kırıldı
  (013'e hiç gelinmedi). Bir kısıtı genişletirken ESKİ tanımını yapan
  dosyadan kaldırın, yalnız yeni (en son) dosyada bırakın.
