# IdeaLode

Çok kaynaklı yazılım fikri öneri pipeline'ı: sosyal/geliştirici
platformlarındaki paylaşımları analiz edip **ayağı yere basan, tek
kişilik/küçük ekiple hızla inşa edilip deploy edilebilecek** yazılım fikri
önerileri (idea card) üretir ve saklar.

Kaynaklar (V1): Hacker News (Algolia Search) + Stack Exchange (softwarerecs,
webapps) aktif; GitHub Issues + Product Hunt Faz 1'de; Reddit wired-ama-uykuda.

## Yapı

- `api/` — Go backend: pipeline (ingest → analyze → synthesize) + REST API (Faz 2).
- `ui/` — Stitch export HTML/CSS/JS ekranlar (Faz 2).
- `scripts/bootstrap_github.sh` — GitHub milestone + issue kurulumu (gh CLI).

## Hızlı başlangıç (Faz 0)

```sh
# 1. Şemayı kur (bkz. api/migrations/README.md)
psql "$DATABASE_URL" -f api/migrations/001_init.sql
psql "$DATABASE_URL" -f api/migrations/002_seed.sql

# 2. Derle
cd api && go build -o idealode ./cmd/idealode

# 3. Konfigürasyon (bkz. .env.example) — en az DATABASE_URL + GROQ_API_KEY

# 4. Çalıştır
./idealode run    # ingest -> analyze -> synthesize
./idealode dump   # idea card'ları JSON olarak incele
```

Üretilen içerik dili `OUTPUT_LANG` ile kontrol edilir (varsayılan `tr`);
alıntılar orijinal dilde kalır, `domain_tags` kanonik EN slug'dır.

## Geliştirme

```sh
cd api
go test ./...                                  # birim testler
TEST_DATABASE_URL=postgres://... go test ./... # + DB entegrasyon testleri
```

Plan ve mimari için V1 Planı (Rev 2) dokümanına, iş sırası için GitHub
issue'larına (Faz 0/1/2 milestone'ları) bakın. **Kalite kapısı**: Faz 0
pipeline'ı ~1 hafta çalışıp ≥30 idea üretmeden ve 10 karttan ≥3'ü "inşa
etmeyi ciddi düşünürüm" seviyesine gelmeden Faz 1'e geçilmez.
