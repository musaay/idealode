#!/usr/bin/env bash
#
# IdeaLode — GitHub bootstrap: milestone'lar (Faz 0/1/2) + kabul kriterli issue'lar.
#
# Gereksinim: `gh` CLI (auth'lu: `gh auth login`).
# Kullanım:   bash scripts/bootstrap_github.sh
#
# Notlar:
# - Idempotent'tir: mevcut milestone/label/issue (başlık eşleşmesiyle) atlanır.
# - Issue'lar SIRAYLA oluşturulur; taze bir repoda (issue/PR yokken) çalıştırılırsa
#   numaralar deterministik olarak #1..#21 olur. Commit mesajları bu numaraları
#   referansladığı için script'i başka issue/PR açmadan ÖNCE çalıştırın.
# - Projects v2 board'u API'den kurulmuyor (bilinçli olarak atlandı) — issue +
#   milestone yeterli.

set -euo pipefail

REPO="${REPO:-musaay/idealode}"

command -v gh >/dev/null 2>&1 || { echo "HATA: gh CLI bulunamadı (https://cli.github.com)"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "HATA: gh auth yok — önce 'gh auth login' çalıştırın"; exit 1; }

echo "==> Repo: $REPO"

# ---------------------------------------------------------------- milestones
ensure_milestone() { # $1=title $2=description
  local num
  num=$(gh api "repos/$REPO/milestones?state=all&per_page=100" \
        --jq ".[] | select(.title == \"$1\") | .number" 2>/dev/null | head -n1)
  if [[ -n "$num" ]]; then
    echo "    milestone var, atlandı: $1 (#$num)"
  else
    num=$(gh api "repos/$REPO/milestones" -f title="$1" -f description="$2" --jq .number)
    echo "    milestone oluşturuldu: $1 (#$num)"
  fi
}

echo "==> Milestone'lar"
ensure_milestone "Faz 0 — Dikey Dilim" \
  "HN (Algolia) + SE (softwarerecs) → ön-filtre → classification → tema → synthesis → Postgres + CLI dump. Auth YOK, UI YOK, tek kullanıcı. Çıkış kriteri: kalite kapısı (~1 hafta, ≥30 idea, 10'da ≥3 'inşa etmeyi ciddi düşünürüm')."
ensure_milestone "Faz 1 — Pipeline'ın Tamamlanması" \
  "GitHub Issues + Product Hunt connector'ları, Reddit (uykuda), pg_trgm dedup, pipeline_runs + advisory lock + stale-run temizliği, cron tetikleme. Faz 0 kalite kapısı geçilmeden BAŞLANMAZ."
ensure_milestone "Faz 2 — Çok Kullanıcılı Katman + UI" \
  "Google Sign-In + app-JWT, roller, REST API'nin tamamı, idea_user_status, refinement chat, comments, build-prompt, generate modları, Stitch UI."

# ------------------------------------------------------------------- labels
echo "==> Label'lar"
gh label create "faz-0" --repo "$REPO" --color "0E8A16" --description "Faz 0 — Dikey Dilim" --force >/dev/null
gh label create "faz-1" --repo "$REPO" --color "FBCA04" --description "Faz 1 — Pipeline'ın Tamamlanması" --force >/dev/null
gh label create "faz-2" --repo "$REPO" --color "1D76DB" --description "Faz 2 — Çok Kullanıcılı Katman + UI" --force >/dev/null
echo "    faz-0 / faz-1 / faz-2 hazır"

# ------------------------------------------------------------------- issues
EXISTING_TITLES=$(gh issue list --repo "$REPO" --state all --limit 200 --json title --jq '.[].title')

create_issue() { # $1=title $2=milestone $3=label ; body stdin'den
  local body
  body=$(cat)
  if grep -Fxq "$1" <<<"$EXISTING_TITLES"; then
    echo "    issue var, atlandı: $1"
    return
  fi
  gh issue create --repo "$REPO" --title "$1" --milestone "$2" --label "$3" --body "$body" >/dev/null
  echo "    issue oluşturuldu: $1"
}

M0="Faz 0 — Dikey Dilim"
M1="Faz 1 — Pipeline'ın Tamamlanması"
M2="Faz 2 — Çok Kullanıcılı Katman + UI"

echo "==> Faz 0 issue'ları"

create_issue "[Faz 0] Go proje iskeleti + konfigürasyon" "$M0" "faz-0" <<'EOF'
Monorepo yapısı (`api/` + `ui/` placeholder) ve Go backend iskeleti. stdlib `net/http` + `pgx` kararına uygun; framework yok, config dosyası YOK (yalnız env).

**Kapsam**
- `api/cmd/idealode/main.go` — tek binary; subcommand iskeleti: `ingest`, `analyze`, `synthesize`, `generate`, `run`, `dump`.
- `api/internal/config` — env: `DATABASE_URL`, `GROQ_API_KEY`, `GROQ_MODEL`, `OUTPUT_LANG` (default `tr`), `STACKEXCHANGE_KEY?`, `GITHUB_TOKEN?`, `PRODUCTHUNT_TOKEN?`, `REDDIT_*` (boş/uykuda), `ADMIN_EMAILS?`, `JWT_SECRET?`.
- `.env.example` — tüm değişkenler açıklamalı.

**Kabul kriterleri**
- [ ] `go build ./...` ve `go vet ./...` temiz geçer.
- [ ] `idealode` argümansız/`-h` subcommand listesini basar; bilinmeyen subcommand açıklayıcı hata verir.
- [ ] Zorunlu env eksikse hangi değişkenin eksik olduğunu söyleyen hata ile çıkar.
- [ ] `OUTPUT_LANG` boşsa `tr` varsayılır.
EOF

create_issue "[Faz 0] Postgres şeması + migration'lar + seed kaynaklar" "$M0" "faz-0" <<'EOF'
Şema BAŞTAN NİHAİ hâliyle kurulur (users/roles dahil) — Faz 0 bir kısmını boş bırakır. Gerçek Postgres şeması `idealode` (tablo-adı prefix'i değil) + bağlantıda `search_path`. Migration: basit `.sql` dosyaları + elle çalıştırma.

**Kapsam**
- `CREATE SCHEMA idealode`; `pg_trgm` extension.
- Tablolar: `users`, `roles` (+seed `admin`/`member`), `user_roles`, `sources`, `raw_posts` (UNIQUE `(platform, source_ref)`), `post_analysis`, `themes` (array kolonu YOK), `theme_posts` (join, PK(theme_id, post_id)), `ideas` (`status`/`build_prompt` kolonu YOK; `monetization_signal` 0-5 int, `urgency_score` 1-5 int), `idea_user_status` (PK(user_id, idea_id); `new` sanal — satır yokluğu), `idea_conversations`, `idea_comments`, `pipeline_runs`.
- Seed `sources` (Rev 2 listesi): SE `softwarerecs`+`webapps`; HN Algolia sorguları ("is there a tool", "I wish there was", "why is there no", "looking for an app", "any alternative to"); GitHub sorguları; PH topic'leri; Reddit seed'leri `active=false`.
- pgx pool: `max_conns ≤ 5`, `search_path=idealode`.

**Kabul kriterleri**
- [ ] Migration'lar boş bir DB'de sırayla hatasız çalışır.
- [ ] `raw_posts` unique constraint'i çift eklemeyi engeller (`ON CONFLICT DO NOTHING` ile idempotent ingest mümkün).
- [ ] Seed sonrası `SELECT count(*) FROM idealode.sources` beklenen kaynak sayısını verir; Reddit satırları `active=false`.
- [ ] Pool limiti 5'i aşmaz (cv-search instance'ı korunur).
EOF

create_issue "[Faz 0] SourceConnector interface + Hacker News (Algolia) connector" "$M0" "faz-0" <<'EOF'
Platform-agnostik ingestion: `SourceConnector` interface — `FetchNew(ctx) ([]RawPost, error)`. Platform eklendikçe `raw_posts` şeması ve üst katmanlar DEĞİŞMEZ.

**Kapsam**
- Keyword araması **Algolia HN Search API** ile (`hn.algolia.com/api` — auth'suz, tam metin + tarih filtresi). Resmi Firebase API'de arama yok; yalnız gerektiğinde item detayı için yedek.
- `sources.community` = kayıtlı arama sorgusu (connector'a özgü selector).
- `last_seen_ref`/timestamp takibi ile yalnızca yeni içerik.

**Kabul kriterleri**
- [ ] Aktif HN kaynakları için Algolia'dan post çekilip `idealode.raw_posts`'a platform-agnostik şemayla yazılır.
- [ ] Aynı `ingest` ikinci kez çalıştığında mevcut kayıtlar çoğalmaz; yalnız yeniler eklenir; `last_seen_ref` güncellenir.
- [ ] HTTP hatalarında açıklayıcı log; tek kaynak hatası diğer kaynakların ingest'ini durdurmaz.
EOF

create_issue "[Faz 0] Stack Exchange connector (softwarerecs + webapps)" "$M0" "faz-0" <<'EOF'
Geniş SE taraması YOK — seçili siteler: `softwarerecs.stackexchange.com` (çekirdek: "şu işi yapan araç var mı?" soruları) + `webapps.stackexchange.com` (ikincil).

**Kapsam**
- `api.stackexchange.com` questions endpoint'i, `sort=creation` + `fromdate` ile artımlı çekim; opsiyonel app key (`STACKEXCHANGE_KEY`).
- API'nin `backoff` alanına uyum (sakin ilerleme).

**Kabul kriterleri**
- [ ] softwarerecs'ten örnek sorular `raw_posts`'a yazılır (title + body + score + url + author).
- [ ] `backoff` alanı geldiğinde belirtilen süre beklenir.
- [ ] İkinci çalıştırmada yalnız yeni sorular eklenir (`last_seen_ref` takibi).
EOF

create_issue "[Faz 0] Ön-filtre (keyword heuristic)" "$M0" "faz-0" <<'EOF'
LLM çağrısı öncesi maliyet azaltma: bariz noise post'ları keyword heuristic ile ele. Keyword'ler İngilizce (kaynak içerik İngilizce) ve öyle kalır.

**Kapsam**
- "I wish", "is there a tool for", "why doesn't X exist", "frustrated with" vb. sinyal kalıpları; title+body üzerinde case-insensitive eşleşme.
- softwarerecs gibi doğal-filtreli kaynaklar için kaynak-bazlı bypass düşünülebilir (sitenin tamamı sinyal).

**Kabul kriterleri**
- [ ] Sinyal içermeyen post'lar `analyze` aşamasına gitmez.
- [ ] Filtre geçen/elenen sayıları loglanır (eşik iterasyonu için görünürlük).
- [ ] Keyword listesi kodda tek yerde, kolay genişletilebilir.
EOF

create_issue "[Faz 0] Groq classification pass (batch + backoff)" "$M0" "faz-0" <<'EOF'
Ön-filtreden geçen post'ları Groq ile sınıflandır; `post_analysis`'e yaz.

**Kapsam**
- Yapılandırılmış çıktı: `classification` (pain_point / feature_request / complaint / noise), `problem_summary` (**TR**), `target_audience` (**TR**), `domain_tags[]` (**kanonik EN slug**, örn. `invoice-automation`), `willingness_to_pay` sinyali.
- Maliyet için batch prompt (chunk'lanmış).
- "Sakin ilerleme": max 3 retry, `1<<attempt` sn backoff, `Retry-After` header'ına uyum; chunk'lar arası 300-500ms sleep (cv-search `reprocess_cvs` hatası tekrarlanmaz: sleep + retry birlikte).

**Kabul kriterleri**
- [ ] Örnek pain_point post'unda beklenen JSON şeması döner; `domain_tags` EN slug, `problem_summary` TR.
- [ ] Geçersiz/parse edilemeyen LLM yanıtı pipeline'ı düşürmez; ilgili chunk loglanıp atlanır.
- [ ] Büyük batch'te chunk'lama manuel testle doğrulanır.
- [ ] Aynı post ikinci kez analiz edilmez (post_analysis varlığına göre).
EOF

create_issue "[Faz 0] Tema gruplama (tag bazlı, embeddingsiz)" "$M0" "faz-0" <<'EOF'
Groq embeddings sunmuyor → V1 embeddingsiz: `domain_tags` + basit anahtar kelime benzerliğiyle gruplama.

**Kapsam**
- `themes` (id, theme_name, description, frequency, first_seen, last_seen — array kolonu YOK) + `theme_posts` join tablosu (FK bütünlüğü; evidence endpoint'i buradan beslenir).
- pain_point / feature_request analizleri temalara bağlanır; frequency/first_seen/last_seen güncellenir.

**Kabul kriterleri**
- [ ] Aynı tag'li post'lar aynı temada toplanır; `theme_posts` üzerinden iki yönlü sorgu çalışır.
- [ ] `frequency` theme_posts sayısıyla tutarlıdır; yeni post'ta `last_seen` güncellenir.
- [ ] noise/complaint sınıfları tema oluşturmaz.
EOF

create_issue "[Faz 0] Idea synthesis (Groq, TR çıktı)" "$M0" "faz-0" <<'EOF'
Frekans eşiğini geçen temalardan idea card üretimi (`source_type=pain_point`).

**Kapsam**
- Ortak prompt kısıtı: "deneyimli bir yazılımcının AI-destekli araçlarla tek başına/küçük ekiple birkaç hafta-ay içinde inşa edip deploy edebileceği, YAZILIM-ağırlıklı, gerçekçi fikirler; donanım/fiziksel altyapı/büyük sermaye/ağır regülasyon YOK; SaaS, API, tarayıcı eklentisi, otomasyon aracı, AI-wrapper vb."
- Çıktı dili `OUTPUT_LANG` (TR): problem_statement, proposed_solution, target_user TR; `example_quotes` orijinal (EN); `domain_tags` EN slug.
- `urgency_score` 1-5 int, `monetization_signal` 0-5 int (0 = sinyal yok).
- Rekabet bilgisi FİLTRE DEĞİL: `known_competitors_ai_guess` alanına "AI tahmini, doğrulanmamış" notuyla yazılır; "zaten biri yapmış" eleme sebebi olamaz.
- Basit dedup (aynı temadan tekrar üretim engeli); tam pg_trgm dedup Faz 1.

**Kabul kriterleri**
- [ ] Eşiği geçen temalar için `ideas` satırı oluşur; TR alanlar TR, tag'ler EN slug, alıntılar orijinal.
- [ ] Skorlar aralık dışına çıkamaz (DB constraint + kod doğrulaması).
- [ ] Aynı tema için tekrar çalıştırma yeni (kopya) idea üretmez.
EOF

create_issue "[Faz 0] Uçtan uca dikey dilim: run + dump CLI" "$M0" "faz-0" <<'EOF'
Faz 0 entegrasyonu: tek komutla ingest → analyze → synthesize; sonuçları basit CLI/JSON dump ile inceleme (auth YOK, UI YOK).

**Kabul kriterleri**
- [ ] `idealode run` üç aşamayı sırayla çalıştırır; her aşamanın işlediği kayıt sayısı loglanır.
- [ ] `idealode dump` idea card'ları okunabilir JSON olarak stdout'a döker (kalite kapısı değerlendirmesi bununla yapılır).
- [ ] Gerçek veriyle (HN + softwarerecs) uçtan uca en az bir idea card üretimi doğrulanır.
EOF

create_issue "[Faz 0] Kalite kapısı değerlendirmesi (Faz 1'e geçiş kriteri)" "$M0" "faz-0" <<'EOF'
En riskli varsayım ürünsel: "pipeline inşa etmeye değer fikirler üretecek mi?" Bu doğrulanmadan Faz 1'e GEÇİLMEZ.

**Kriter**
- Pipeline ~1 hafta çalışır, en az ~30 idea card üretir.
- Kullanıcı değerlendirmesinde her 10 karttan **en az 3'ü** "bunu inşa etmeyi ciddi olarak düşünürüm" seviyesinde.

**Kabul kriterleri**
- [ ] ≥30 idea card üretildi.
- [ ] Değerlendirme yapıldı; 10'da ≥3 eşiği geçildi → Faz 1 açılır.
- [ ] Geçilmediyse: prompt/kaynak/eşik iterasyonu yapıldı ve yeniden değerlendirildi (bu issue kapanmadan Faz 1 issue'larına başlanmaz).
EOF

echo "==> Faz 1 issue'ları"

create_issue "[Faz 1] GitHub Issues connector (advanced_search=true)" "$M1" "faz-1" <<'EOF'
`search/issues` API ile TÜM public repo'larda anahtar kelime taraması. **Mart 2025 API değişikliği**: çağrılar `advanced_search=true` parametresiyle yapılır.

**Kapsam**
- `sources.community` = search query (örn. `label:"feature request" state:open`, `"i wish" in:body is:issue`).
- Opsiyonel PAT (`GITHUB_TOKEN`): 60/saat → 5000/saat; search 30/dk limitine "sakin" uyum.
- `willingness_to_pay` sinyali zayıf, `pain_point`/`feature_request` güçlü — beklenti bu.

**Kabul kriterleri**
- [ ] `advanced_search=true` ile beklenen sonuçlar döner ve `raw_posts`'a yazılır.
- [ ] Rate limit aşımında backoff; ikinci çalıştırmada yalnız yeni issue'lar.
EOF

create_issue "[Faz 1] Product Hunt connector (GraphQL v2)" "$M1" "faz-1" <<'EOF'
Launch + yorumlar; yorumlardaki "I wish it also did X" tarzı boşluk sinyalleri hedeflenir.

**Kapsam**
- GraphQL v2, developer token (`PRODUCTHUNT_TOKEN`); `sources.community` = topic slug (seed: developer-tools, saas, productivity, artificial-intelligence).

**Kabul kriterleri**
- [ ] Topic bazlı launch + top yorumlar `raw_posts`'a yazılır.
- [ ] Token yoksa connector açıklayıcı logla atlanır (pipeline düşmez).
EOF

create_issue "[Faz 1] Reddit connector (wired / uykuda)" "$M1" "faz-1" <<'EOF'
Reddit "Responsible Builder Policy" nedeniyle yeni script-app OAuth onayı vermiyor. Connector kod olarak HAZIR yazılır, `REDDIT_*` env boş bırakılır — erişim açılırsa yalnız env doldurulup aktive edilir. Plan kapsamından ÇIKARILMADI; yakın vadede açılacağına dair işaret de yok (beklenti yönetimi).

**Kabul kriterleri**
- [ ] Resmi OAuth2 (script app) akışı kodda tam; env boşken connector "uykuda" logu ile atlanır.
- [ ] Seed Reddit kaynakları DB'de `active=false` durur.
- [ ] Data API Terms uyumu (rate limit, attribution) kodda hazır.
EOF

create_issue "[Faz 1] pg_trgm idea dedup + gri bölge LLM kontrolü" "$M1" "faz-1" <<'EOF'
Yeni idea yazılmadan önce iki katmanlı dedup.

**Kapsam**
- `pg_trgm similarity()` — title + normalize problem_statement; eşik ≥ ~0.55 → duplicate, yazma.
- Gri bölge (~0.40-0.55) → tek ucuz Groq çağrısı: "bu ikisi aynı fikir mi?"
- Eşikler gerçek veriyle kalibre edilir (0.40/0.55 başlangıç).

**Kabul kriterleri**
- [ ] Aynı temanın tekrar yeni idea üretmediği doğrulanır.
- [ ] Bariz kopyalar pg_trgm eşiğinde yakalanır; gri bölge LLM'e düşer.
- [ ] Eşikler env/config ile ayarlanabilir (kalibrasyon için).
EOF

create_issue "[Faz 1] pipeline_runs + advisory lock + stale-run temizliği" "$M1" "faz-1" <<'EOF'
Eşzamanlılık ve görünürlük altyapısı.

**Kapsam**
- Her job başında `pg_try_advisory_lock(hash(job_type))`; alınamazsa "zaten çalışıyor" logu ile çıkış.
- Binary açılışında (ve her `run` başında): 6 saatten eski `running` kayıtları `failed (stale)` işaretlenir.
- Her job başlangıç/bitişinde `pipeline_runs` kaydı (job_type, started_at, finished_at, status, items_processed, error_message).

**Kabul kriterleri**
- [ ] Aynı job iki kez paralel tetiklendiğinde ikincisi çalışmadan çıkar.
- [ ] Stale `running` kaydı açılışta `failed` işaretlenir.
- [ ] Başarılı/başarısız koşular `pipeline_runs`'ta doğru status ve sayaçlarla görünür.
EOF

create_issue "[Faz 1] Cron ile otomatik \"sakin\" tetikleme" "$M1" "faz-1" <<'EOF'
Railway cron veya OS cron ile periyodik tetikleme — V1 için yeterli; "Search Agents" yok.

**Kapsam**
- ingest günde 2-4; classify/synthesize günde 1-2.
- Cron tetiklemesi rol kısıtına tabi DEĞİL (manuel tetikleme Faz 2'de admin'e kısıtlanır).

**Kabul kriterleri**
- [ ] Cron tanımları repo'da dokümante (örn. Railway cron config / crontab örneği).
- [ ] Üst üste binen tetiklemeler advisory lock sayesinde çakışmaz.
EOF

echo "==> Faz 2 issue'ları"

create_issue "[Faz 2] Google Sign-In + app-JWT + rol yönetimi" "$M2" "faz-2" <<'EOF'
Hibrit çok kullanıcılı model (~10 kullanıcı varsayımı).

**Kapsam**
- `POST /api/auth/google`: Google ID token doğrulama → `users` bul/oluştur → `ADMIN_EMAILS` allowlist idempotent kontrol → app-JWT (rol claim'li) dön.
- JWT: **7 gün, refresh YOK** — süre dolunca Google ile sessiz yeniden giriş. Bilinen trade-off: rol değişikliği token ömrü boyunca yansımaz (acilse imza key rotate).
- Roller: normalize `roles`/`user_roles`; her yeni kullanıcıya otomatik `member`; `admin` → sources yazma + manuel pipeline tetikleme; kısıt YALNIZ paylaşılan altyapıda.
- `X-API-Key` tamamen kalkar.

**Kabul kriterleri**
- [ ] Yeni kullanıcıda `users` kaydı + otomatik `member` rolü.
- [ ] `ADMIN_EMAILS` dışındaki kullanıcı sources yazma ve `POST /api/pipeline/run`'da 403 alır; listedeki erişir.
- [ ] JWT süresi dolunca yeniden giriş akışı çalışır.
EOF

create_issue "[Faz 2] REST API uçları (sources, ideas, pipeline, health)" "$M2" "faz-2" <<'EOF'
stdlib `net/http` ile V1 API'nin tamamı.

**Kapsam**
- `GET /api/sources` (herkes); `POST/PATCH/DELETE /api/sources` (**admin**) — config dosyası YOK, sistem sektör-agnostik.
- `GET /api/ideas` — paylaşılan havuz + kendi `idea_user_status` (LEFT JOIN + COALESCE, sanal `new`). Query: `status`, `tag`, `min_evidence`, `source_type`, `sort`, `has_build_prompt`, `limit`/`offset`.
- `POST /api/ideas` (`source_type=user_created`); `GET /api/ideas/{id}` (efektif kişisel görünüm); `PATCH /api/ideas/{id}/status` (upsert; `new`'e çekmek = status'u NULL'a çekmek).
- `GET /api/ideas/{id}/evidence` (theme_posts üzerinden); `GET /api/pipeline/status`; `POST /api/pipeline/run` (**admin**); `GET /api/health`.

**Kabul kriterleri**
- [ ] `/api/sources` üzerinden kaynak ekle/çıkar → sonraki `ingest` bunu yansıtır.
- [ ] Satırı olmayan kullanıcıya idea `new` görünür; filtreler doğru çalışır.
- [ ] Rol kısıtları yalnız belirtilen uçlarda; member tüm kişisel uçlara erişir.
EOF

create_issue "[Faz 2] idea_user_status + refinement chat + comments" "$M2" "faz-2" <<'EOF'
Tamamen kişiye özel keşif katmanı. Paylaşılan `ideas` satırı "tohum" — chat ile ASLA değişmez.

**Kapsam**
- `POST /api/ideas/{id}/chat` (`{message}`): mesajı `idea_conversations`'a kaydet → Groq context (base idea + kullanıcının `refined_*` + kendi geçmişi) → `{reply, updated_fields}` → doluysa yalnız istek sahibinin `refined_*` alanları güncellenir. Yanıt dili TR (`OUTPUT_LANG`); Groq retry+backoff pattern'i.
- `POST /api/ideas/{id}/comments` (`{text}`) — kullanıcıya özel düz not.
- `idea_conversations.role` = mesaj yazarı (`user`|`assistant`) — yetki rolüyle karıştırılmaz.

**Kabul kriterleri**
- [ ] İki kullanıcı aynı idea'da ayrı sohbet — her biri yalnız kendi geçmişini görür.
- [ ] `updated_fields` istek sahibinin `refined_*`'ını günceller; `ideas` satırı DEĞİŞMEZ.
- [ ] Biri `favorited`, diğeri `rejected` — birbirini etkilemez; üçüncü kullanıcıya `new` döner.
EOF

create_issue "[Faz 2] build-prompt + generate modları (ai_generated / ai_blended)" "$M2" "faz-2" <<'EOF'
**Kapsam**
- `POST /api/ideas/{id}/build-prompt`: Cursor/Copilot/v0/Lovable için kısa PRD/prompt taslağı — **İngilizce** (PO kararı). Tek LLM çağrısı, sadece istenince; sonuç `idea_user_status.refined_build_prompt`'a (kullanıcıya özel).
- `generate` job'ı: aktif kullanıcılar (son 14 günde girişi olan) üzerinde döner. `ai_generated`: kullanıcı bazlı few-shot (favorited/rejected); üretilen idea paylaşılan havuza `created_by_user_id` ile. `ai_blended`: serbest yaratıcılık + son dönem tema özetleri.

**Kabul kriterleri**
- [ ] build-prompt EN üretilir ve yalnız istek sahibinde saklanır.
- [ ] `generate` yalnız aktif kullanıcılar için döner; few-shot kullanıcının kendi feedback'inden beslenir.
- [ ] Üretilen idea'lar dedup'tan geçer (Faz 1 mekanizması).
EOF

create_issue "[Faz 2] Stitch UI ekranları (ui/)" "$M2" "faz-2" <<'EOF'
Stitch'ten export edilen ham HTML/CSS/JS ekranlar — framework YOK, V1 basit. **Uygulama dili Türkçe** (tek dil, sabit metin; i18n V2).

**Kapsam**
- `ui/` klasörü; `api/` REST uçlarını tüketir; JWT saklar, `Authorization: Bearer` gönderir.
- Read + triage akışları: idea listesi (filtreler), idea detay (efektif görünüm), chat, sources yönetimi (admin), pipeline status.
- Ekran listesi Faz 2 başlamadan plana eklenecek (bkz. plan: "Stitch UI ekranları bölümü ayrıca eklenecek").

**Kabul kriterleri**
- [ ] Google Sign-In → JWT → korumalı uçlara erişim akışı çalışır.
- [ ] Idea triage (status değiştirme, favori, build-prompt) uçtan uca UI'dan yapılabilir.
EOF

echo "==> Bitti. Kontrol: gh issue list --repo $REPO --limit 30"
