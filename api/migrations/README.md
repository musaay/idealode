# Migration'lar

Karar (plan, "Sonraki Adımlar" #2): basit `.sql` dosyaları + **elle çalıştırma**.
Migration aracı yok.

Sıra numarasına göre uygulanır; dosyalar idempotent yazılmıştır
(`IF NOT EXISTS` / `ON CONFLICT DO NOTHING`), tekrar çalıştırmak güvenlidir:

```sh
psql "$DATABASE_URL" -f api/migrations/001_init.sql
psql "$DATABASE_URL" -f api/migrations/002_seed.sql
```

Notlar:

- Veriler gerçek `idealode` Postgres şemasında yaşar (tablo-adı prefix'i
  değil); uygulama bağlantısı `search_path=idealode,public` set eder.
- `pg_trgm` extension'ı gerekir (idea dedup) — 001 içinde
  `CREATE EXTENSION IF NOT EXISTS` var; Railway Postgres'te izinlidir.
- cv-search ile aynı instance paylaşıldığı için uygulama pool'u
  `max_conns ≤ 5` ile sınırlıdır (bkz. `api/internal/store`).
