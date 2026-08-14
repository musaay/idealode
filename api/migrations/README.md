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
psql "$DATABASE_URL" -f api/migrations/001_init.sql
psql "$DATABASE_URL" -f api/migrations/002_seed.sql
```

Notlar:

- Tüm nesneler `idealode` Postgres şemasında yaşar; uygulama bağlantısı
  `search_path=idealode,public` set eder.
- `pg_trgm` extension'ı gerekir — 001 içinde `CREATE EXTENSION IF NOT
  EXISTS` var.
