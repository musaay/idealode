# Faz 2 — Dilim 1b: `idealode api` + web'in API istemcisine geçişi

Sahip: lead. Uygulayan: `developer` (api) + `ui-developer` (istemci), PARALEL,
ayrı worktree'lerde. Doğrulayan: reviewer + qa.
İlgili: #18 (api), #21 (web). Önceki spec: `faz2-dilim1-web.md`.

## Kural (mimari)
UI DB'ye dokunmaz. Tek binary, üç süreç:
- `run` — pipeline (cron). Değişmez.
- `api` — JSON API; `DATABASE_URL`'i gören TEK sunucu süreci.
- `serve` — HTML render; yalnız `API_BASE_URL` görür, DB bağlantısı açmaz.

## Sözleşme (her iki taraf buna göre yazar; değiştirmek lead onayı ister)
Base: `API_BASE_URL` (ör. `http://idealode-api.railway.internal:8080`). Tüm yanıtlar
`Content-Type: application/json; charset=utf-8`. Hata gövdesi: `{"error":"not_found"}`
(`code` değerleri: `not_found`, `bad_request`, `internal`). Tarihler RFC3339 UTC.

```
GET /healthz                       → 200 {"status":"ok"}
GET /api/ideas?source_type=&q=&limit=
    → 200 {"ideas":[Idea...]}      (boşsa {"ideas":[]}; limit<=0 → varsayılan 60, >200 → 200;
                                   bilinmeyen source_type → 200 {"ideas":[]}; q 200 karaktere kırpılır)
GET /api/ideas/{id}                → 200 {"idea":Idea} | 404 not_found (geçersiz id de 404)
GET /api/ideas/{id}/sources        → 200 {"sources":[IdeaSource...]} | 404 (kart yoksa)
```
`Idea` = `store.Idea`'nın mevcut JSON etiketleri (id, title, problem_statement,
proposed_solution, target_user, evidence_count, example_quotes, source_type,
source_theme_id, urgency_score, monetization_signal, known_competitors_ai_guess,
domain_tags, source_theme, local_evidence, created_at). nil slice → `[]` (asla `null`).
`IdeaSource` JSON: `{"platform","community","url","created_at"}`; sıfır zaman → `created_at` alanı atlanır (`omitempty` + zero check).

## developer — `api` (dosyalar)
```
api/internal/api/server.go        NewServer(store) *http.Server-benzeri; router (Go 1.22 pattern), JSON yardımcıları, log + recover + 5s handler timeout
api/internal/api/handlers.go      listIdeas, getIdea, ideaSources, healthz; parametre doğrulama yukarıdaki sözleşmeyle birebir
api/internal/api/server_test.go   httptest + fake store: her uç için 200/404/boş liste/limit sınırları/bilinmeyen source_type/geçersiz id; nil slice → [] testi
api/internal/store/models.go      IdeaSource'a json etiketleri (+ created_at omitempty davranışı için MarshalJSON veya view tipi)
api/cmd/idealode/main.go          `api` subcommand (PORT, varsayılan 8080) + usage; `serve` artık DB açmaz: API_BASE_URL okur, apiclient.New(base, 5*time.Second) ile web.NewServer'ı besler (ui-developer'ın paketi gelene kadar derlenmez; imza aşağıda sabit)
README.md                         api komutu + env (API_BASE_URL, DATABASE_URL hangi süreçte)
```
`api` süreci `store.IdeaStore`-benzeri arayüzü kendi tanımlar (fake ile test).

## ui-developer — istemci (dosyalar)
```
api/internal/apiclient/client.go      package apiclient; func New(baseURL string, timeout time.Duration) *Client
                                      Client web.IdeaStore'u uygular: ListIdeasFiltered, GetIdea, IdeaSources
                                      404 → store.ErrNotFound; ağ/5xx/bozuk JSON → sarılı hata (%w) ; ctx iptali saygı
api/internal/apiclient/client_test.go httptest sahte API: mutlu yol, 404→ErrNotFound, 500, timeout, bozuk JSON, boş liste
api/internal/web/handlers.go          API hatası (ErrNotFound dışı) → 502 şablonlu sayfa ("Servis şu an yanıt vermiyor"), log
api/internal/web/templates/error.html 502 varyantı; i18n anahtarı (tr/en eşit)
api/internal/web/web_test.go          502 yolu testi
```
`main.go`'ya DOKUNMAZ (developer'ın). `store` paketine dokunmaz; `store.ErrNotFound`'u import eder.

## Kabul kriterleri
1. `idealode api` ayağa kalkar; sözleşmedeki 4 uç birebir (qa curl ile doğrular).
2. `idealode serve` DATABASE_URL olmadan çalışır; API kapalıyken `/` 502 şablonlu sayfa döner, süreç düşmez.
3. Galeri/detay/kaynaklar çıktısı dilim 1 ile aynı (fake API ile web testleri geçer; dilim 1'in 33 testi yeşil kalır).
4. nil slice hiçbir uçta `null` üretmez.
5. build/vet/test yeşil; hiçbir test canlı DB/ağ istemez.
6. Public repo hijyeni.

## Deploy (lead)
Railway: yeni `idealode-api` servisi (start `/app/idealode api`, DATABASE_URL referans, public domain YOK);
`idealode-web`: DATABASE_URL kaldır, `API_BASE_URL=http://idealode-api.railway.internal:8080`.
