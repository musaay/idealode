package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/musaay/idealode/ui/internal/web"
)

// #178: bu fixture'lar backend/internal/api/golden_test.go tarafından
// gerçek handler'lardan ÜRETİLİR/DOĞRULANIR (backend tarafındaki test
// fixture eskirse kırılır). Burada AYNI dosyalar apiclient'in gerçek ayrıştırma
// yoluyla (Client.*, msgDTO/sourceDTO dahil) okunur — sözleşmenin iki
// taraftan da birebir olduğu böylece doğrulanır. Fixture'ları elle
// bozmayın: kasıtlı sözleşme değişikliği backend tarafında
// UPDATE_GOLDEN=1 ile yeniden üretilmeli, burası öyle otomatik güncellenmez.
const testdataDir = "testdata"

// mustReadFixture, testdata/name dosyasını okur; eksikse test hemen düşer
// (fixture'ların iki modülde de senkron tutulması gerektiğinin erken sinyali).
func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(testdataDir + "/" + name)
	if err != nil {
		t.Fatalf("fixture okunamadı (%s): %v — backend/internal/api/golden_test.go ile senkron olmalı", name, err)
	}
	return b
}

// fixtureClient, verilen yoldaki isteğe fixture dosyasının ham gövdesiyle
// cevap veren sahte API + ona bağlı gerçek apiclient.Client döner.
func fixtureClient(t *testing.T, path string, fixture []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 2*time.Second)
}

func TestGolden_ListIdeasParsesFixture(t *testing.T) {
	fx := mustReadFixture(t, "ideas_list.json")
	c := fixtureClient(t, "/api/ideas", fx)

	ideas, err := c.ListIdeasFiltered(context.Background(), web.IdeaFilter{})
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if len(ideas) != 3 {
		t.Fatalf("kart sayısı = %d, 3 bekleniyor (fixture değişti mi?)", len(ideas))
	}

	organic, blended, full := ideas[0], ideas[1], ideas[2]

	if organic.ID != 601 || organic.Slug != "kart-601" || organic.SourceType != "pain_point" {
		t.Errorf("organic kart yanlış çözüldü: %+v", organic)
	}
	if organic.Mine {
		t.Errorf("organic kart mine=true olmamalı")
	}

	if blended.ID != 602 || blended.SourceType != "ai_blended" || !blended.Mine {
		t.Errorf("türetilmiş kart yanlış çözüldü: %+v", blended)
	}
	if blended.ParentIdeaID == nil || *blended.ParentIdeaID != 601 || blended.ParentSlug != "kart-601" {
		t.Errorf("parent alanları yanlış çözüldü: %+v", blended)
	}

	if full.ID != 501 || full.Slug != "randevu-botu-9x8y" {
		t.Fatalf("tam kart yanlış çözüldü: %+v", full)
	}
	if full.SourceThemeID == nil || *full.SourceThemeID != 12 {
		t.Errorf("source_theme_id çözülmedi: %+v", full.SourceThemeID)
	}
	if full.DistinctivenessVerdict == nil || *full.DistinctivenessVerdict != "unsure" {
		t.Errorf("distinctiveness_verdict çözülmedi: %+v", full.DistinctivenessVerdict)
	}
	if full.DataAccessVerdict == nil || *full.DataAccessVerdict != "pass" {
		t.Errorf("data_access_verdict çözülmedi: %+v", full.DataAccessVerdict)
	}
	if len(full.LensVerdicts) != 1 || full.LensVerdicts[0].Lens != "veri-erişimi" || full.LensVerdicts[0].Verdict != "pass" {
		t.Errorf("lens_verdicts çözülmedi: %+v", full.LensVerdicts)
	}
	if full.PublishedAt == nil || full.PublishedAt.IsZero() {
		t.Errorf("published_at çözülmedi: %+v", full.PublishedAt)
	}
	if full.FusedAt == nil || full.FusedAt.IsZero() {
		t.Errorf("fused_at çözülmedi: %+v", full.FusedAt)
	}
}

func TestGolden_GetIdeaParsesFixture(t *testing.T) {
	fx := mustReadFixture(t, "idea_full.json")
	c := fixtureClient(t, "/api/ideas/randevu-botu-9x8y", fx)

	idea, err := c.GetIdeaBySlug(context.Background(), "randevu-botu-9x8y")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if idea.ID != 501 || idea.Title != "Randevu Botu" || idea.EvidenceCount != 6 {
		t.Errorf("kart alanları yanlış çözüldü: %+v", idea)
	}
	if len(idea.ExampleQuotes) != 2 || len(idea.DomainTags) != 2 || len(idea.LocalEvidence) != 1 {
		t.Errorf("dizi alanları yanlış çözüldü: %+v", idea)
	}
	if idea.KnownCompetitorsAIGuess != "Calendly, Acuity Scheduling" {
		t.Errorf("known_competitors_ai_guess yanlış: %q", idea.KnownCompetitorsAIGuess)
	}
}

func TestGolden_IdeaSourcesParsesFixture(t *testing.T) {
	fx := mustReadFixture(t, "sources.json")
	c := fixtureClient(t, "/api/ideas/kart-601/sources", fx)

	src, err := c.IdeaSources(context.Background(), "kart-601")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if len(src) != 2 {
		t.Fatalf("kaynak sayısı = %d, 2 bekleniyor", len(src))
	}
	if src[0].Platform != "hackernews" || src[0].CreatedAt.IsZero() {
		t.Errorf("ilk kaynak yanlış çözüldü: %+v", src[0])
	}
	if src[1].Platform != "radar_seed" || !src[1].CreatedAt.IsZero() {
		t.Errorf("created_at atlanan kaynak yanlış çözüldü: %+v", src[1])
	}
}

func TestGolden_ChatParsesFixture(t *testing.T) {
	fx := mustReadFixture(t, "chat_list.json")
	c := fixtureClient(t, "/api/ideas/kart-601/chat", fx)

	msgs, err := c.ListChat(context.Background(), "kart-601")
	if err != nil {
		t.Fatalf("beklenmeyen hata: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("mesaj sayısı = %d, 2 bekleniyor", len(msgs))
	}
	// msgDTO.ID (backend'de int64/BIGSERIAL) tel üzerinde sayı, ui'de
	// web.ChatMessage.ID string'e çevrilir (#110 deseniyle aynı: sayısal
	// id web katmanına sızmaz).
	if msgs[0].ID != "9001" || msgs[0].Role != "user" {
		t.Errorf("ilk mesaj yanlış çözüldü: %+v", msgs[0])
	}
	if msgs[1].ID != "9002" || msgs[1].Role != "assistant" || msgs[1].Message == "" {
		t.Errorf("ikinci mesaj yanlış çözüldü: %+v", msgs[1])
	}
}
