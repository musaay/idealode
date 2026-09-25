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

#178'den beri backend ve ui İKİ AYRI Go modülü (ayrı `go.mod`, ayrı deploy) —
ui backend'i import ETMEZ, yalnız HTTP (`API_BASE_URL`) ile konuşur.

- `backend/` — Go modülü (`github.com/musaay/idealode/backend`): pipeline + REST API
- `backend/internal/api/` — JSON API sunucusu (`idealode api`); DATABASE_URL'i
  gören TEK süreç (#18)
- `ui/` — Go modülü (`github.com/musaay/idealode/ui`): sunucuda render edilen
  web arayüzü
- `ui/internal/web/` — web arayüzü (`ui/cmd/web`, eski adıyla `idealode serve`);
  DB'ye bağlanmaz, kartları `apiclient` ile API'den okur
- `ui/internal/apiclient/` — web katmanının API'ye konuştuğu HTTP istemcisi;
  backend'in store türlerini import etmez, kendi DTO kopyalarını taşır
  (`ui/internal/web/models.go`) — sözleşme backend'deki golden test ile korunur
- `scripts/` — yardımcı scriptler

## Kurulum

Gereksinimler: Go 1.22+, PostgreSQL (`pg_trgm` extension'ı ile), OpenAI-uyumlu
chat-completions endpoint'i sunan bir LLM sağlayıcısının API anahtarı
(varsayılan: [Groq](https://groq.com); `LLM_BASE_URL` ile başka bir
sağlayıcıya geçilebilir, kod değişikliği gerekmez — bkz. `.env.example`).

```sh
# 1. Derle (iki ayrı modül, iki ayrı binary)
cd backend && go build -o idealode ./cmd/idealode && cd ..
cd ui && go build -o web ./cmd/web && cd ..

# 2. Konfigürasyon — .env.example'ı kopyalayıp doldur
#    (en az DATABASE_URL + LLM_API_KEY; backend/ kökünde ya da env'de)

# 3. Veritabanı şemasını kur
./backend/idealode migrate

# 4. Pipeline'ı çalıştır
./backend/idealode run    # ingest -> analyze -> synthesize
./backend/idealode dump   # üretilen idea card'ları JSON olarak incele

# 5. JSON API'yi aç (DATABASE_URL'i gören TEK süreç)
./backend/idealode api    # http://localhost:8080 (ör. PORT=8081 ile ayrı port)

# 6. Web arayüzünü aç (salt okunur galeri + kart detayı) — API'nin adresini
#    gösterir, kendi DB bağlantısı YOK
API_BASE_URL=http://localhost:8081 ./ui/web  # http://localhost:8080
```

Adımlar tek tek de çalıştırılabilir: `./idealode ingest`, `analyze`,
`synthesize`.

## JSON API

`./idealode api` — pipeline'ın ürettiği idea card'ları JSON olarak sunar
(#18) ve kart sohbeti/"Idea Copilot"u (#66) çalıştırır. `DATABASE_URL`'i
gören TEK süreçtir; ui (`ui/cmd/web`) dahil hiçbir başka süreç veritabanına
doğrudan bağlanmaz. LLM'e yalnız bu süreç gider — `LLM_API_KEY` (geriye uyumlu:
`GROQ_API_KEY`) `api` için de zorunludur; `LLM_BASE_URL`/`LLM_MODEL`
isteğe bağlıdır (varsayılan sağlayıcı: Groq). Sıcaklık politikası (#106):
yargı çağrıları (sınıflandırma, tutarlılık/dedup/mercek/hakem kararları)
sıcaklık 0 ile tutarlı karar üretir, kart/sohbet metni üreten çağrılar
0.3'te çeşitlilik için kalır. Adres: `PORT` ortam değişkeni
(varsayılan `8080`). Uçlar:

```
GET  /healthz                       → 200 {"status":"ok"}
GET  /api/ideas?source_type=&q=&limit=
GET  /api/ideas/{id}
GET  /api/ideas/{id}/sources
GET  /api/ideas/{id}/chat           → kart sohbeti geçmişi
POST /api/ideas/{id}/chat           → {"message","lang"} -> {"reply","suggestions"}
POST /api/ideas/{id}/blend          → sohbetten yeni `ai_blended` kart türetir
```

`/healthz` dışındaki tüm uçlar `X-Session-Id: <hex>` başlığı ister (girişsiz
kimlik — anonim oturum çerezi; ui üretir). Tüm yanıtlar
`application/json; charset=utf-8`; hata gövdesi `{"error":"not_found"|
"bad_request"|"internal"|"rate_limited"|"upstream"|"no_conversation"}`. Boş
liste her zaman `[]` döner, asla `null` (nil slice'lar sözleşme sınırında
`[]`'e indirgenir). Sohbet kotaları (oturum başına, süreç belleğinde):
30 mesaj/saat, 5 blend/gün. `ai_blended` kartlar yalnız üreten oturuma
görünür (galeri + detay); başkasının kartı 404 döner. Ayrıntılı sözleşme:
`docs/specs/faz2-dilim1b-api.md`, `docs/specs/faz2-dilim2-chat.md`.

Public domain almaz — Railway'de yalnız iç ağda (`idealode-web` servisinden)
erişilir; dışa açık uç ui'dır.

## Web arayüzü

`ui/cmd/web` (`./ui/web` binary'si) kart havuzunu web'den okunur kılar:
galeri (kaynak türü filtresi + arama) ve kart detayı (problem, çözüm,
birebir alıntılar, yerel talep kanıtı, kaynak linkleri). Salt okunurdur —
giriş, tepki ve sohbet sonraki dilimlerde. Ayrı Go modülü — backend'i import
etmez, yalnız `apiclient` üzerinden HTTP ile konuşur.

- **DB'ye bağlanmaz** — kartları `API_BASE_URL` üzerinden `idealode api`'den
  okur (zorunlu ortam değişkeni; ör. `http://<api-iç-adres>:8080`).
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
`api` (hepsi backend modülünde). ui DATABASE_URL görmez/kullanmaz; onun
yerine `API_BASE_URL` zorunludur (`idealode api`'nin adresi).

## Geliştirme

```sh
cd backend
go test ./...                                  # birim testler
TEST_DATABASE_URL=postgres://... go test ./... # + DB entegrasyon testleri

cd ../ui
go test ./...                                  # birim testler (DB'ye dokunmaz)
```
