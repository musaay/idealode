# Faz 2 — Dilim 1: `idealode serve` (galeri + kart detayı, salt okunur)

Sahip: lead (PO). Uygulayan: `ui-developer`. Doğrulayan: `reviewer` + `qa` (paralel).
İlgili issue'lar: #21 (UI), #18 (API — bu dilimde yalnız okuma uçları).
Tasarım referansı: https://github.com/musaay/idealode-ui (kod alınmaz, token/yerleşim okunur).

## Amaç
Kart havuzunu web'den okunur kılmak: galeri (filtre + arama) ve kart detayı
(problem, çözüm, kanıtlar, yerel talep). Kimlik doğrulama, tepki (beğen/kaydet)
ve sohbet bu dilimde YOK; arayüzde yerleri boş bırakılmaz, hiç gösterilmez.

## Kapsam dışı (dilim 2+)
Google Sign-In / JWT (#17), idea_user_status ve tepkiler (#19), sohbet →
ai_blended (#20), kaynak yönetimi ve pipeline tetikleme (#18 admin uçları).

## Mimari kararlar
- Tek binary, yeni subcommand: `idealode serve`. `PORT` env (varsayılan 8080).
  Railway'de ayrı bir servis olarak `serve` ile koşar; cron servisi `run`da kalır.
- `net/http` stdlib (Go 1.22+ pattern router: `GET /ideas/{id}`).
- `html/template` + `embed.FS`; şablonlar başlangıçta bir kez parse edilir.
- CSS el yazımı, tek dosya; JS vanilla, tek dosya, progressive enhancement.
- i18n: TR/EN JSON katalogları embed; dil seçimi `?lang=` → cookie `lang`;
  yoksa `Accept-Language`; yoksa `tr`. Tema: cookie `theme` (`light|dark`),
  yoksa `prefers-color-scheme`.
- Veri okuma: mevcut `store.Store` üzerinden yeni sorgular; web katmanı
  store'a **arayüz** üzerinden bağlanır ki handler testleri fake ile koşsun.

## Dosya yol haritası
```
api/cmd/idealode/main.go                 serve subcommand + usage satırı
api/internal/web/server.go               NewServer(deps) *http.Server, router, middleware (log, recover, security headers)
api/internal/web/handlers.go             handleGallery, handleIdea, handleHealth, handleStatic, notFound/error render
api/internal/web/view.go                 view modelleri: GalleryPage, IdeaPage, EvidenceItem; local_evidence satır parser'ı
api/internal/web/i18n.go                 katalog yükleme, t() template func, dil çözümü
api/internal/web/templates/layout.html   <html lang data-theme>, head, header (logo, dil TR/EN, tema), footer
api/internal/web/templates/gallery.html  filtre chipleri, arama, kart grid, boş durum
api/internal/web/templates/idea.html     başlık bloğu, problem, çözüm, kanıtlar, yerel talep, kaynaklar
api/internal/web/templates/error.html    404 / 500
api/internal/web/static/app.css          token'lar (:root + [data-theme=dark]), layout, bileşenler
api/internal/web/static/app.js           tema/dil anahtarı (cookie yaz + reload), filtre chip aktif durumu; JS'siz çalışır
api/internal/web/i18n/tr.json, en.json   anahtar kümeleri EŞİT olmalı (test)
api/internal/store/queries.go            ListIdeasFiltered, GetIdea, IdeaSources (aşağıda)
api/internal/store/models.go             Idea'ya LocalEvidence []string ekle; ListIdeas SELECT'ine local_evidence
api/internal/web/*_test.go               handler (httptest), şablon parse, i18n eşitlik, local_evidence parser testleri
Dockerfile                               değişmez (CMD run kalır); railway: ikinci servis startCommand "/app/idealode serve"
README.md                                serve komutu + env
```

## Store sorguları (developer değil, ui-developer yazar; reviewer store tuzaklarına bakar)
- `ListIdeasFiltered(ctx, f IdeaFilter) ([]Idea, error)` — `f.SourceType` (boş = hepsi),
  `f.Query` (title/problem ILIKE), `f.Limit` (varsayılan 60). Sıra: created_at DESC.
  `local_evidence` dahil. nil slice guard'ları korunur (DomainTags, ExampleQuotes, LocalEvidence boş `[]`).
- `GetIdea(ctx, id) (*Idea, error)` — yoksa `store.ErrNotFound` (yeni sentinel).
- `IdeaSources(ctx, ideaID) ([]IdeaSource, error)` — kartın kaynak gönderileri:
  `ideas.source_theme_id → theme_posts → raw_posts` (platform, url, community,
  created_at/fetched_at); market_derived kartlarda tema yoktur, o zaman
  `raw_posts WHERE platform='radar_seed'` eşleşmesi seeds.go'daki `source_ref`
  kuralıyla yapılır (uygulayan seeds.go'ya bakar; bulamazsa lead'e sorar).
  En fazla 10 satır, tarih DESC.

## Tasarım yetkisi (ui-developer aynı zamanda UI designer'dır)
Aşağıdaki görünüm modeli asgari çerçevedir; ui-developer referans tasarıma
bakarak boşlukları kendi doldurur — bileşen ekler, yerleşimi değiştirir,
boş/hata durumlarını tasarlar. Her şeyin hazır verilmesini beklemez.
Bu yetki BOŞLUKLAR içindir, genel temayı değiştirmek için değil: renk paleti,
tipografi, kart yapısı, yerleşim, spacing ve köşe ölçeği referans tasarımdan
birebir alınır; kişisel tercih uygulanmaz. Değişmez sınırlar: veri uydurulmaz
(yazar, oy, pazar büyüklüğü, öncelik gibi DB'de olmayan alan gösterilmez),
alıntı birebir kalır, kaynak linki gerçek URL'dir. Bu sınırlar dışındaki tasarım kararları raporda "tasarım kararı"
başlığıyla listelenir; lead onaylamazsa geri alınır.

## Görünüm modeli (asgari)
- Rozet: `pain_point` → yeşil "Ağrı Noktası / Pain point"; `market_derived` →
  amber "Pazar Verisi / Market-derived"; `ai_blended` → mor "AI Karışım / AI-blended";
  diğer source_type'lar nötr gri, ham değer. Kartta TEK rozet.
- Kanıt sayısı: `evidence_count` ("N kanıt / N evidence").
- Kanıtlar bölümü: `example_quotes` birebir, tırnak içinde, çeviri YOK, kırpma YOK.
  Alıntı başına link yok (veri modelinde eşleme yok); bölümün altında
  **Kaynaklar** listesi: `IdeaSources` satırları (platform etiketi + kısaltılmış
  URL + tarih, `rel="noopener noreferrer" target="_blank"`).
- Yerel talep: `local_evidence` satırları `"<alıntı> — [<platform>] <url>"`
  biçiminde; parser bunu ayırır, ayıramazsa satırı olduğu gibi alıntı olarak
  gösterir. Boşsa boş durum metni.
- Domain etiketleri nötr chip. Tarih `YYYY-MM-DD`.
- Galeri filtreleri: chip = `<a href="/?source_type=...">` (aktif chip
  `aria-current="page"`); arama `<form method="get">`. Sonuç sayısı başlıkta.

## Kabul kriterleri
1. `idealode serve` ayağa kalkar; `GET /healthz` 200 `{"status":"ok"}`.
2. `GET /` kartları yeniden eskiye listeler; `?source_type=` ve `?q=` çalışır;
   sonuç yoksa boş durum görünür (200).
3. `GET /ideas/{id}` kartı gösterir; yok → 404 sayfası (şablonlu); geçersiz id → 404.
4. 390 / 768 / 1280 px'te yatay kaydırma yok; galeri 1 / 2 / 3 sütun.
5. TR/EN anahtarı çalışır (cookie kalıcı); kart içeriği değişmez.
6. Açık/koyu tema anahtarı çalışır; `prefers-color-scheme` varsayılan.
7. JS kapalıyken filtre, arama, detay, dil (`?lang=`) çalışır.
8. Alıntılar DB'deki metinle bayt bayt aynı (test: fake store'daki alıntı HTML'de escape'lenmiş halde birebir bulunur; `<script>` içeren alıntı çalıştırılmaz).
9. Kaynak linkleri `noopener noreferrer`; dış CDN'den script yok; `Content-Security-Policy` başlığı `default-src 'self'; style-src 'self' fonts.googleapis.com; font-src fonts.gstatic.com`.
10. `go build/vet/test` yeşil; handler testleri fake store ile canlı DB'siz koşar.

## Definition of Done
- Yukarıdaki 10 kriter PASS (qa raporunda kriter başına kanıt).
- reviewer bulguları kapatıldı (özellikle: autoescape, nil slice, sorgu N+1 yok, şablon parse istek başına değil).
- Migration gerekmedi (gerekirse çift kopya + embed kuralı).
- README güncel; public repo hijyeni.
- Lead canlı DB ile görsel kontrol yaptı (Railway preview veya lokal `DATABASE_URL` ile).
