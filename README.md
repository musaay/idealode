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
- `api/internal/web/` — sunucuda render edilen web arayüzü (`idealode serve`)
- `ui/` — tasarım/prototip notları (arayüzün kendisi `api/internal/web/` altındadır)
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

# 5. Web arayüzünü aç (salt okunur galeri + kart detayı)
./idealode serve  # http://localhost:8080
```

Adımlar tek tek de çalıştırılabilir: `./idealode ingest`, `analyze`,
`synthesize`.

## Web arayüzü

`./idealode serve` kart havuzunu web'den okunur kılar: galeri (kaynak türü
filtresi + arama) ve kart detayı (problem, çözüm, birebir alıntılar, yerel
talep kanıtı, kaynak linkleri). Salt okunurdur — giriş, tepki ve sohbet
sonraki dilimlerde.

- Adres: `PORT` ortam değişkeni, varsayılan `8080`. Sağlık kontrolü:
  `GET /healthz`.
- Sunucuda render edilir (Go `html/template`); şablonlar, CSS/JS ve TR/EN
  mesaj katalogları binary'ye gömülüdür — ayrı bir frontend build'i yoktur.
- Arayüz dili TR/EN olarak değiştirilebilir (`?lang=`, cookie'de saklanır);
  kart içeriği ve alıntılar çevrilmez. Tema `prefers-color-scheme` ile gelir,
  kullanıcı seçimi (`?theme=`) bunu ezer.
- JavaScript kapalıyken de tam çalışır: filtreler, arama, dil ve tema
  bağlantıları düz `<a>`/`<form>` öğeleridir.

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
