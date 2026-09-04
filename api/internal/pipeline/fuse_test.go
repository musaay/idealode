package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/store"
)

func TestFuseJudgeSelectsMatches(t *testing.T) {
	idea := store.Idea{Title: "KOBİ Lead Takibi", ProblemStatement: "Esnaf lead'leri DM'de kaybediyor"}
	candidates := []store.RawPost{
		{Title: "instagram mesajları takip edilemiyor", Body: "dm den gelen siparişleri unutuyorum", Platform: "googleplay", URL: "u0"},
		{Title: "alakasız post", Body: "oyun çöküyor", Platform: "hackernews", URL: "u1"},
		{Title: "whatsapp satış derdi", Body: "müşteri listesi tutamıyorum", Platform: "technopat", URL: "u2"},
	}
	chat := &indicesChat{response: `{"indices":[0,2]}`}

	matched, err := fuseJudge(context.Background(), chat, idea, candidates)
	if err != nil {
		t.Fatalf("fuseJudge: %v", err)
	}
	if len(matched) != 2 || matched[0].URL != "u0" || matched[1].URL != "u2" {
		t.Errorf("beklenen [u0 u2], geldi: %+v", matched)
	}
}

func TestFuseJudgeEmptyResult(t *testing.T) {
	chat := &indicesChat{response: `{"indices":[]}`}
	matched, err := fuseJudge(context.Background(), chat, store.Idea{Title: "X"},
		[]store.RawPost{{Title: "a"}})
	if err != nil {
		t.Fatalf("fuseJudge: %v", err)
	}
	if len(matched) != 0 {
		t.Errorf("boş eşleşme beklenirdi, geldi: %d", len(matched))
	}
}

func TestFuseJudgePromptContainsIdeaAndPosts(t *testing.T) {
	rec := &recordingChat{response: `{"indices":[]}`}
	idea := store.Idea{Title: "Başlık A", ProblemStatement: "Problem B"}
	if _, err := fuseJudge(context.Background(), rec, idea,
		[]store.RawPost{{Title: "Post C", Body: "Gövde D"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Başlık A", "Problem B", "[0] Post C", "Gövde D"} {
		if !strings.Contains(rec.lastUser, want) {
			t.Errorf("hakem prompt'unda %q bekleniyordu", want)
		}
	}
}

// recordingChat, gönderilen user prompt'unu kaydeder.
type recordingChat struct {
	response string
	lastUser string
}

func (c *recordingChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	c.lastUser = user
	return c.response, nil
}

// scriptedChat, talep hakemi (fuseJudgeSystem) ile ivme hakemi
// (fuseMomentumSystem) için AYRI cevap/hata döndürür — reviewer bulgusunu
// kilitleyen testlerde (ivme hakemi hata verirken talep hakemi başarılı
// olsun diye) kullanılır.
type scriptedChat struct {
	demandResponse   string
	demandErr        error
	momentumResponse string
	momentumErr      error
}

func (c *scriptedChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	if system == fuseMomentumSystem {
		if c.momentumErr != nil {
			return "", c.momentumErr
		}
		return c.momentumResponse, nil
	}
	if c.demandErr != nil {
		return "", c.demandErr
	}
	return c.demandResponse, nil
}

// fakeFuseStore, FuseEvidence'ın yazdığı çağrıları kaydeden sabit-cevaplı
// fuseStore sahtesi (canlı DB olmadan orkestrasyon testleri için).
type fakeFuseStore struct {
	ideas      []store.Idea
	candidates []store.RawPost // FusionCandidates döner
	momentum   []store.RawPost // MomentumCandidates döner

	setCalls    []setCall
	appendCalls []appendCall
}

type setCall struct {
	ideaID   int64
	evidence []string
}

type appendCall struct {
	ideaID int64
	lines  []string
}

func (f *fakeFuseStore) IdeasNeedingFusion(ctx context.Context, limit int) ([]store.Idea, error) {
	return f.ideas, nil
}

func (f *fakeFuseStore) FusionCandidates(ctx context.Context, tags []string, problem string, limit int) ([]store.RawPost, error) {
	return f.candidates, nil
}

func (f *fakeFuseStore) SetIdeaLocalEvidence(ctx context.Context, ideaID int64, evidence []string) error {
	f.setCalls = append(f.setCalls, setCall{ideaID, evidence})
	return nil
}

func (f *fakeFuseStore) MomentumCandidates(ctx context.Context, problem string, tags []string, limit int) ([]store.RawPost, error) {
	return f.momentum, nil
}

func (f *fakeFuseStore) AppendIdeaLocalEvidence(ctx context.Context, ideaID int64, lines []string) error {
	f.appendCalls = append(f.appendCalls, appendCall{ideaID, lines})
	return nil
}

// TestFuseEvidenceFirstPassMomentumErrorKeepsDemand, reviewer bulgusunu
// kilitler: ilk geçişte (fused_at boş) ivme hakemi hata verirse talep
// kanıtı YİNE de tek SetIdeaLocalEvidence çağrısıyla yazılır (Append hiç
// çağrılmaz) — kanıt kaybolmaz, yalnız ivme haftalık yeniden denemeye kalır.
func TestFuseEvidenceFirstPassMomentumErrorKeepsDemand(t *testing.T) {
	idea := store.Idea{ID: 1, Title: "KOBİ Lead Takibi", ProblemStatement: "Esnaf lead'leri DM'de kaybediyor"}
	fs := &fakeFuseStore{
		ideas: []store.Idea{idea},
		candidates: []store.RawPost{
			{Title: "whatsapp satış derdi", Body: "müşteri listesi tutamıyorum", Platform: "technopat", URL: "u0"},
		},
		momentum: []store.RawPost{
			{Title: "owner/repo", Body: "★100 · crm aracı", Platform: "github_trending", URL: "gh0", Score: 5},
		},
	}
	chat := &scriptedChat{
		demandResponse: `{"indices":[0]}`,
		momentumErr:    errors.New("groq: 500"),
	}

	n, err := FuseEvidence(context.Background(), &config.Config{}, fs, chat)
	if err != nil {
		t.Fatalf("FuseEvidence: %v", err)
	}
	if n != 1 {
		t.Errorf("beklenen 1 füzyonlanmış kart, geldi: %d", n)
	}
	if len(fs.appendCalls) != 0 {
		t.Fatalf("ivme hatasında AppendIdeaLocalEvidence hiç çağrılmamalı, geldi: %+v", fs.appendCalls)
	}
	if len(fs.setCalls) != 1 {
		t.Fatalf("tek SetIdeaLocalEvidence (tek UPDATE) beklenirdi, geldi: %d", len(fs.setCalls))
	}
	evidence := fs.setCalls[0].evidence
	if len(evidence) != 1 || !strings.Contains(evidence[0], "müşteri listesi tutamıyorum") {
		t.Errorf("yalnız talep satırı beklenirdi, geldi: %+v", evidence)
	}
}

// TestFuseEvidenceFirstPassCombinesDemandAndMomentum, ilk geçişte hem talep
// hem ivme hakemi başarılı olduğunda TEK SetIdeaLocalEvidence çağrısıyla,
// talep satırları ÖNCE ivme satırları (en fazla 2) SONRA sırayla yazıldığını
// doğrular.
func TestFuseEvidenceFirstPassCombinesDemandAndMomentum(t *testing.T) {
	idea := store.Idea{ID: 1, Title: "KOBİ Lead Takibi", ProblemStatement: "Esnaf lead'leri DM'de kaybediyor"}
	fs := &fakeFuseStore{
		ideas: []store.Idea{idea},
		candidates: []store.RawPost{
			{Title: "whatsapp satış derdi", Body: "müşteri listesi tutamıyorum", Platform: "technopat", URL: "u0"},
		},
		momentum: []store.RawPost{
			{Title: "owner/repo", Body: "★100 · crm aracı", Platform: "github_trending", URL: "gh0", Score: 5},
		},
	}
	chat := &scriptedChat{
		demandResponse:   `{"indices":[0]}`,
		momentumResponse: `{"indices":[0]}`,
	}

	n, err := FuseEvidence(context.Background(), &config.Config{}, fs, chat)
	if err != nil {
		t.Fatalf("FuseEvidence: %v", err)
	}
	if n != 1 {
		t.Errorf("beklenen 1 füzyonlanmış kart, geldi: %d", n)
	}
	if len(fs.appendCalls) != 0 {
		t.Fatalf("ilk geçişte AppendIdeaLocalEvidence hiç çağrılmamalı, geldi: %+v", fs.appendCalls)
	}
	if len(fs.setCalls) != 1 {
		t.Fatalf("tek SetIdeaLocalEvidence (tek UPDATE) beklenirdi, geldi: %d", len(fs.setCalls))
	}
	evidence := fs.setCalls[0].evidence
	if len(evidence) != 2 {
		t.Fatalf("talep + ivme = 2 satır beklenirdi, geldi: %+v", evidence)
	}
	if !strings.Contains(evidence[0], "müşteri listesi tutamıyorum") {
		t.Errorf("0. satır talep kanıtı olmalı, geldi: %q", evidence[0])
	}
	if !strings.Contains(evidence[1], "owner/repo") || !strings.Contains(evidence[1], "★100") ||
		!strings.Contains(evidence[1], "+5/gün") || !strings.Contains(evidence[1], "[github_trending]") {
		t.Errorf("1. satır ivme kanıtı biçiminde olmalı, geldi: %q", evidence[1])
	}
}
