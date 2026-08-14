# IdeaLode

Çok kaynaklı yazılım fikri öneri pipeline'ı: geliştirici ve sosyal
platformlardaki paylaşımları toplar, LLM ile analiz edip temalara gruplar ve
küçük ekiplerin hızla inşa edebileceği yazılım fikri kartları (idea card)
üretir.

**Kaynaklar:** Hacker News ve Stack Exchange aktif; GitHub Issues, Product
Hunt ve Reddit connector'ları yolda.

## Nasıl çalışır

```
ingest  →  analyze  →  synthesize
(kaynaklardan  (LLM ile sınıflandırma  (temalardan idea
 post topla)    + tema gruplama)         card üretimi)
```

## Proje yapısı

- `api/` — Go backend: pipeline + REST API
- `ui/` — web arayüzü
- `scripts/` — yardımcı scriptler

## Kurulum

Gereksinimler: Go 1.22+, PostgreSQL (`pg_trgm` extension'ı ile), bir
[Groq](https://groq.com) API anahtarı.

```sh
# 1. Derle
cd api && go build -o idealode ./cmd/idealode

# 2. Konfigürasyon — .env.example'ı kopyalayıp doldur
#    (en az DATABASE_URL + GROQ_API_KEY)

# 3. Veritabanı şemasını kur
./idealode migrate

# 4. Pipeline'ı çalıştır
./idealode run    # ingest -> analyze -> synthesize
./idealode dump   # üretilen idea card'ları JSON olarak incele
```

Adımlar tek tek de çalıştırılabilir: `./idealode ingest`, `analyze`,
`synthesize`.

## Konfigürasyon

Tüm ayarlar ortam değişkeniyle verilir; liste ve açıklamalar için
[`.env.example`](.env.example) dosyasına bakın. Üretilen içeriğin dili
`OUTPUT_LANG` ile seçilir (varsayılan `tr`); alıntılar orijinal dilde kalır.

## Geliştirme

```sh
cd api
go test ./...                                  # birim testler
TEST_DATABASE_URL=postgres://... go test ./... # + DB entegrasyon testleri
```
