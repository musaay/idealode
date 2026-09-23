# IdeaLode — geliştirme rehberi

Go pipeline (ingest → analyze → synthesize → fuse): sosyal/geliştirici
platformlardan toplanan paylaşımlardan LLM ile doğrulanmış yazılım fikri
kartları üretir. Ayrıntı: README.md.

## Komutlar
#178'den beri backend (`github.com/musaay/idealode/backend`, pipeline/store/api)
ve ui (`github.com/musaay/idealode/ui`, web arayüzü) iki AYRI Go modülü —
ui backend'i import ETMEZ, yalnız HTTP (API_BASE_URL) ile konuşur.
- Backend build/test: `cd backend && go build ./... && go vet ./... && go test ./...`
  (DB testleri `TEST_DATABASE_URL` yoksa atlanır. Gerçek DB'ye karşı koşarken
  `go test -p 1 ./...` kullan: store ve pipeline aynı DB'yi paylaşıyor, paket
  paralelliği kilit kuyruğu yaratıyor. CI bunu zaten böyle koşuyor.)
- ui build/test: `cd ui && go build ./... && go vet ./... && go test ./...`
  (DB'ye bağlanmaz, dış bağımlılığı yok — testler apiclient'i httptest ile sınar.)
- Migration uygulama: `idealode migrate` (elle tetiklenir, otomatik çalışmaz; backend'den)
- Pipeline: `idealode run` (= ingest + analyze + synthesize + fuse; advisory lock'lu; backend'den)
- Web arayüzü: eski `idealode serve` alt-komutu kaldırıldı, yerine `ui/cmd/web`
  binary'si — PORT ve zorunlu API_BASE_URL env'iyle ayağa kalkar.

## Konvansiyonlar
- Yorumlar ve log mesajları Türkçe; domain_tags/prompt'lar İngilizce.
- Migration'lar İKİ kopya tutulur: `backend/migrations/` + `backend/internal/store/migrate_sql/`
  ve `migrate.go`'da embed + Exec zincirine eklenir. Her migration idempotent
  olmalı (tüm dosyalar her koşuda yeniden çalıştırılır).
- ui, backend'in store.Idea/IdeaSource/IdeaFilter/ErrNotFound/FlagDoubtful
  türlerini `ui/internal/web/models.go`'da JSON sözleşmesine göre birebir
  yansıtır (import yok, ayrı modül). Sözleşme kopukluğuna karşı
  `backend/internal/api/golden_test.go` gerçek handler JSON'unu
  `ui/internal/apiclient/testdata/*.json` fixture'larıyla karşılaştırır
  (golden) — backend alan eklerse/değiştirirse bu test kırılır; kasıtlıysa
  `UPDATE_GOLDEN=1` ile fixture yeniden üretilir ve ui tarafındaki
  `ui/internal/apiclient/golden_test.go` ile birlikte gözden geçirilir.
- nil slice tuzağı: pgx nil `[]string`'i SQL NULL yazar, `NOT NULL DEFAULT '{}'`
  kolonlarını kırar — insert sınırında guard var, koru.
- LLM cevapları savunmacı parse edilir (bitişik indeksler, boş cevap, skip).
- Connector'lar: `last_seen_ref` incremental cursor + "sakin ilerleme"
  (istek/sayfa limitleri). İlk çekim penceresi 7 gün.
- Bu repo PUBLIC: secret, iç altyapı detayı, kişisel veri commit edilmez.

## Team workflow
Esaslı işler (özellik, düzeltme, refactor) HER ZAMAN ajan takımıyla yürür,
solo değil: `.claude/agents/`dan teammate spawn edilir — `developer`
(pipeline/store) veya `ui-developer` (web katmanı) uygular (kaynağı
düzenleyen TEK rol), ardından `reviewer` doğrular; canlı doğrulama
(DB, curl, tarayıcı) lead'de —
ana oturum lead'dir: spec'i yazar (dosya seviyesinde yol haritası + kabul
kriterleri), raporları süzer, kullanıcıya tek onay özeti sunar. git/commit/
push/deploy yalnız lead'dedir. Ayrı qa rolü YOK (2026-09-02'de kaldırıldı:
reviewer + lead'in canlı kontrolü yeterli). Teammate'ler yalnız lead'le konuşur (yıldız
topolojisi). Solo çalışma yalnız şunlarda kabul: önemsiz tek satırlıklar,
salt araştırma/soru-cevap, takımın yapamayacağı infra/ops işleri (git,
deploy, canlı DB/Groq işlemleri). 

LLM prompt'una dokunan her iş (mercek, sentez, kümeleme, tohum kartı) spec'te
ALTIN SET taşır: gerçek kart/tohum id'leri + beklenen verdict; lead bu
set üzerindeki canlı sonucu issue/PR'a ekler, reviewer set yoksa işi keser
(reviewer.md madde 6). Sahte chat testi tesisatı sınar, yargıyı sınamaz.
Altın set EN AZ 3 koşu ölçülür (`idealode lens-ab --runs 3`); canlı sürüm
ve aday aynı sette, en fazla 48 saat arayla ölçülür. Aday (a) blok kararı
tekrarlanabilirliğinde canlıdan kötü olamaz, (b) pass beklenen kartlarda
(PO'nun tuttukları) canlıdan fazla blok üretemez; tek koşuluk ölçüm kabul
edilmez (#166 dersi, #182). Mutlak %95 eşiği canlının kendisi %90 ölçüldüğü
için göreliye çevrildi (#181). Sağlayıcı/model/oylama değişikliği de prompt
değişikliği sayılır. Canlı ölçümü lead koşar (developer canlı LLM'e
dokunmaz), sonucu issue/PR'a ekler.
Mercek/prompt denetimi her ayın 1'i ve 15'inde otomatik issue ile takvime
bağlıdır (.github/workflows/lens-audit.yml).

İş takibi: her iş (önemsiz tek satırlıklar hariç) önce GitHub issue olur,
"IDEA LODE" project board'unda In progress'e çekilir (`gh project item-edit`),
PR açıklaması `Closes #n` taşır; merge sonrası kart Done'a düşer. Board'da
In progress boşken kimse çalışmıyor demektir.
