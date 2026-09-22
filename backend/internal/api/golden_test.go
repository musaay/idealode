package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/musaay/idealode/backend/internal/store"
)

// #178: ui, backend'i artık import ETMEZ (ayrı Go modülü) — bu yüzden
// ui/internal/apiclient/web.Idea/IdeaSource/... türleri store.Idea/
// IdeaSource'un JSON sözleşmesini elle yansıtır. Bu dosya, gerçek
// handler'lardan üretilen JSON'u ui/internal/apiclient/testdata/ altındaki
// checked-in fixture'larla karşılaştırır: fixture eskirse (backend alan
// eklerse/değiştirirse) BU TEST kırılır — ui tarafındaki golden_test.go
// aynı fixture'ları web.* türlerine çözümleyerek sözleşmenin iki taraftan
// da birebir olduğunu doğrular.
//
// Fixture'ları yeniden üretmek için (yalnız elle, kasıtlı sözleşme
// değişikliğinde): UPDATE_GOLDEN=1 go test ./internal/api/... -run Golden

var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// goldenFixturePath, ui/internal/apiclient/testdata'ya giden göreli yol.
// İki modül ayrı Go modülü olduğundan (import yok) paylaşım yalnız dosya
// sistemi üzerinden, checked-in JSON dosyalarıyla kurulur.
func goldenFixturePath(name string) string {
	return filepath.Join("..", "..", "..", "ui", "internal", "apiclient", "testdata", name)
}

// assertGolden, got'u fixturePath'teki JSON ile SEMANTİK olarak
// karşılaştırır (anahtar sırası/boşluk önemsiz, alan/değer birebir
// eşleşmeli).
func assertGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	path := goldenFixturePath(name)

	if updateGolden {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, got, "", "  "); err != nil {
			t.Fatalf("fixture biçimlendirilemedi (%s): %v", name, err)
		}
		pretty.WriteByte('\n')
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatalf("fixture yazılamadı (%s): %v", name, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture okunamadı (%s): %v — önce UPDATE_GOLDEN=1 ile üret", path, err)
	}

	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("üretilen JSON çözümlenemedi: %v\n%s", err, got)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatalf("fixture JSON çözümlenemedi (%s): %v", path, err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("API yanıtı fixture'dan (%s) sapmış — ui/internal/apiclient sözleşmesi eskimiş olabilir "+
			"(kasıtlıysa UPDATE_GOLDEN=1 ile yeniden üret).\nüretilen: %s\nbeklenen (fixture): %s", path, got, want)
	}
}

// ptrStr/ptrInt64/ptrTime, golden fixture'larda opsiyonel (pointer) alanları
// doldurmak için — server_test.go'daki store literal'leri bunlara ihtiyaç
// duymuyordu, bu dosyaya özeldir.
func ptrStr(v string) *string        { return &v }
func ptrInt64(v int64) *int64        { return &v }
func ptrTime(v time.Time) *time.Time { return &v }

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("zaman ayrıştırılamadı (%s): %v", s, err)
	}
	return tm
}

// goldenStore, golden testler için sabit (deterministik) veri seti.
func goldenStore(t *testing.T) *fakeStore {
	t.Helper()

	organic := store.Idea{
		ID:                 601,
		Slug:               "kart-601",
		Title:              "Kart 601",
		ProblemStatement:   "Basit bir dert tanımı.",
		ProposedSolution:   "Basit bir çözüm.",
		TargetUser:         "Herkes",
		EvidenceCount:      2,
		ExampleQuotes:      []string{"örnek alıntı"},
		SourceType:         "pain_point",
		DomainTags:         []string{"smb"},
		LocalEvidence:      []string{},
		UrgencyScore:       3,
		MonetizationSignal: 1,
		CreatedAt:          mustParseTime(t, "2026-09-01T08:00:00Z"),
	}
	blended := store.Idea{
		ID:                 602,
		Slug:               "kart-602",
		Title:              "Kart 602 (türetilmiş)",
		ProblemStatement:   "Sohbetten türetilen dert.",
		ProposedSolution:   "Sohbetten türetilen çözüm.",
		TargetUser:         "KOBİ",
		EvidenceCount:      2,
		ExampleQuotes:      []string{"örnek alıntı"},
		SourceType:         "ai_blended",
		DomainTags:         []string{"smb"},
		LocalEvidence:      []string{},
		ParentIdeaID:       ptrInt64(601),
		ParentSlug:         "kart-601",
		CreatedBySessionID: testSID,
		UrgencyScore:       3,
		MonetizationSignal: 1,
		CreatedAt:          mustParseTime(t, "2026-09-03T09:00:00Z"),
	}

	full := store.Idea{
		ID:                      501,
		Slug:                    "randevu-botu-9x8y",
		Title:                   "Randevu Botu",
		ProblemStatement:        "KOBİ'ler müşteri randevularını elle takip ediyor ve sık sık çakışma yaşıyor.",
		ProposedSolution:        "WhatsApp üzerinden otomatik randevu hatırlatma ve çakışma kontrolü sağlayan bot.",
		TargetUser:              "Küçük işletme sahipleri",
		EvidenceCount:           6,
		ExampleQuotes:           []string{"Randevularımı takip edemiyorum", "Sürekli çakışma yaşıyorum"},
		SourceType:              "market_derived",
		SourceThemeID:           ptrInt64(12),
		UrgencyScore:            4,
		MonetizationSignal:      3,
		KnownCompetitorsAIGuess: "Calendly, Acuity Scheduling",
		DomainTags:              []string{"smb", "scheduling"},
		LocalEvidence:           []string{"yerel kanıt satırı"},
		SourceTheme:             "Randevu Yönetimi",
		CreatedAt:               mustParseTime(t, "2026-09-01T10:00:00Z"),
		FusedAt:                 ptrTime(mustParseTime(t, "2026-09-02T08:00:00Z")),
		DistinctivenessVerdict:  ptrStr("unsure"),
		DistinctivenessReason:   ptrStr("Benzer ürünler var ama fark belirsiz."),
		DataAccessVerdict:       ptrStr("pass"),
		DataAccessReason:        ptrStr(""),
		LensVerdicts: []store.LensVerdict{
			{
				Lens:          "veri-erişimi",
				PromptVersion: "v1",
				Subject:       "card",
				Verdict:       "pass",
				Reason:        "Veri erişimi üçüncü taraf API üzerinden sağlanıyor.",
				At:            mustParseTime(t, "2026-09-01T09:55:00Z"),
			},
			{
				Lens:          "özgünlük",
				PromptVersion: "v4",
				Subject:       "card",
				Verdict:       "pass",
				Reason:        "Yerel açı yeterli.",
				At:            mustParseTime(t, "2026-09-01T09:56:00Z"),
				Model:         "gemini-3.5-flash-lite",
			},
		},
		PublishedAt: ptrTime(mustParseTime(t, "2026-09-01T10:05:00Z")),
	}

	return &fakeStore{
		ideas:  []store.Idea{organic, blended, full},
		bySlug: map[string]*store.Idea{organic.Slug: &organic, blended.Slug: &blended, full.Slug: &full},
		sources: map[int64][]store.IdeaSource{
			organic.ID: {
				{Platform: "hackernews", Community: "news", URL: "https://example.com/a",
					CreatedAt: mustParseTime(t, "2026-08-20T09:30:00Z")},
				{Platform: "radar_seed", Community: "", URL: "https://example.com/b"}, // CreatedAt sıfır -> alan atlanır
			},
		},
		chat: map[int64]map[string][]store.ChatMessage{
			organic.ID: {
				testSID: {
					{ID: 9001, Role: "user", Message: "Bu fikri nasıl geliştirebilirim?",
						CreatedAt: mustParseTime(t, "2026-09-05T10:00:00Z")},
					{ID: 9002, Role: "assistant", Message: "Önce hedef kullanıcıyı netleştir.",
						CreatedAt: mustParseTime(t, "2026-09-05T10:00:05Z")},
				},
			},
		},
	}
}

func TestGolden_ListIdeas(t *testing.T) {
	s := newTestServer(goldenStore(t), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertGolden(t, rec.Body.Bytes(), "ideas_list.json")
}

func TestGolden_GetIdea(t *testing.T) {
	s := newTestServer(goldenStore(t), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/randevu-botu-9x8y")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertGolden(t, rec.Body.Bytes(), "idea_full.json")
}

func TestGolden_IdeaSources(t *testing.T) {
	s := newTestServer(goldenStore(t), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/kart-601/sources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertGolden(t, rec.Body.Bytes(), "sources.json")
}

func TestGolden_Chat(t *testing.T) {
	s := newTestServer(goldenStore(t), &fakeLLM{})
	rec := doReq(t, s.Handler(), "/api/ideas/kart-601/chat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	assertGolden(t, rec.Body.Bytes(), "chat_list.json")
}
