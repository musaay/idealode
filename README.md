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
- `api/internal/api/` — JSON API sunucusu (`idealode api`); DATABASE_URL'i
  gören TEK süreç (#18)
- `api/internal/apiclient/` — web katmanının API'ye konuştuğu HTTP istemcisi
- `api/internal/web/` — sunucuda render edilen web arayüzü (`idealode serve`);
  DB'ye bağlanmaz, kartları `apiclient` ile API'den okur
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

# 5. JSON API'yi aç (DATABASE_URL'i gören TEK süreç)
./idealode api    # http://localhost:8080 (ör. PORT=8081 ile ayrı port)

# 6. Web arayüzünü aç (salt okunur galeri + kart detayı) — API'nin adresini
#    gösterir, kendi DB bağlantısı YOK
API_BASE_URL=http://localhost:8081 ./idealode serve  # http://localhost:8080
```

Adımlar tek tek de çalıştırılabilir: `./idealode ingest`, `analyze`,
`synthesize`.

## JSON API

`./idealode api` — pipeline'ın ürettiği idea card'ları JSON olarak sunar
(#18). `DATABASE_URL`'i gören TEK süreçtir; `serve` dahil hiçbir başka süreç
veritabanına doğrudan bağlanmaz. Adres: `PORT` ortam değişkeni (varsayılan
`8080`). Uçlar:

```
GET /healthz                       → 200 {"status":"ok"}
GET /api/ideas?source_type=&q=&limit=
GET /api/ideas/{id}
GET /api/ideas/{id}/sources
```

Tüm yanıtlar `application/json; charset=utf-8`; hata gövdesi
`{"error":"not_found"|"bad_request"|"internal"}`. Boş liste her zaman `[]`
döner, asla `null` (nil slice'lar sözleşme sınırında `[]`'e indirgenir).
Ayrıntılı sözleşme: `docs/specs/faz2-dilim1b-api.md`.

Public domain almaz — Railway'de yalnız iç ağda (`idealode-web` servisinden)
erişilir; dışa açık uç `serve`'dür.

## Web arayüzü

`./idealode serve` kart havuzunu web'den okunur kılar: galeri (kaynak türü
filtresi + arama) ve kart detayı (problem, çözüm, birebir alıntılar, yerel
talep kanıtı, kaynak linkleri). Salt okunurdur — giriş, tepki ve sohbet
sonraki dilimlerde.

- **DB'ye bağlanmaz** — kartları `API_BASE_URL` üzerinden `idealode api`'den
  okur (zorunlu ortam değişkeni; ör. `http://idealode-api.railway.internal:8080`).
  API kapalıyken/yanıt vermezken galeri ve kart sayfaları 502 şablonlu bir
  hata sayfası gösterir, süreç düşmez.
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

`DATABASE_URL` yalnız veritabanına doğrudan bağlanan süreçlerde zorunludur:
`ingest`, `analyze`, `synthesize`, `fuse`, `seeds`, `run`, `migrate`, `dump`,
`api`. `serve` DATABASE_URL görmez/kullanmaz; onun yerine `API_BASE_URL`
zorunludur (`idealode api`'nin adresi).

## Geliştirme

```sh
cd api
go test ./...                                  # birim testler
TEST_DATABASE_URL=postgres://... go test ./... # + DB entegrasyon testleri
```
