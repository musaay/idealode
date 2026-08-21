package pipeline

import (
	"context"
	"strings"
	"testing"

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
