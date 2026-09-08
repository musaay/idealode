package pipeline

import (
	"context"
	"reflect"
	"testing"

	"github.com/musaay/idealode/api/internal/config"
	"github.com/musaay/idealode/api/internal/store"
)

// tempRecordingChat, yalnız son çağrının sıcaklığını kaydeden minimal
// sahte Chat (#106 doğrulaması için).
type tempRecordingChat struct {
	response string
	lastTemp float64
}

func (c *tempRecordingChat) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, 0.3)
}

func (c *tempRecordingChat) ChatJSONWithTemperature(ctx context.Context, system, user string, temp float64) (string, error) {
	c.lastTemp = temp
	return c.response, nil
}

// TestClassifyChunkUsesTemperatureZero, sınıflandırma yargı çağrısının
// (#106) sıcaklık 0 ile gittiğini doğrular.
func TestClassifyChunkUsesTemperatureZero(t *testing.T) {
	chat := &tempRecordingChat{response: `{"results":[{"id":1,"classification":"noise"}]}`}
	cfg := &config.Config{OutputLang: "tr"}
	if _, err := classifyChunk(context.Background(), cfg, chat, []store.RawPost{{ID: 1}}); err != nil {
		t.Fatalf("classifyChunk: %v", err)
	}
	if chat.lastTemp != 0 {
		t.Errorf("classifyChunk sıcaklık 0 ile çağırmalı, geldi: %v", chat.lastTemp)
	}
}

func TestNormalizeTags(t *testing.T) {
	in := []string{"Invoice Automation", "invoice-automation", "AI/ML", "", "a", "b", "c", "d"}
	want := []string{"invoice-automation", "ai-ml", "a", "b", "c"} // dedup + max 5
	if got := normalizeTags(in); !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeTags = %v, beklenen %v", got, want)
	}
}

func TestParseClassifyResponse(t *testing.T) {
	chunk := []store.RawPost{{ID: 1}, {ID: 2}}
	raw := `{"results":[
		{"id":1,"classification":"PAIN_POINT","problem_summary":"Fatura takibi zor","target_audience":"serbest çalışanlar","domain_tags":["Invoice Automation"],"willingness_to_pay":true},
		{"id":2,"classification":"bogus-value","problem_summary":"","target_audience":"","domain_tags":[]},
		{"id":99,"classification":"pain_point"},
		{"id":1,"classification":"noise"}
	]}`

	out, err := parseClassifyResponse(raw, chunk)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("2 sonuç beklendi (id=99 dışarıda, id=1 tekrar sayılmaz), geldi: %d", len(out))
	}
	if out[0].PostID != 1 || out[0].Classification != "pain_point" || !out[0].WillingnessToPay {
		t.Errorf("ilk sonuç hatalı: %+v", out[0])
	}
	if out[0].DomainTags[0] != "invoice-automation" {
		t.Errorf("tag slug'lanmalı: %v", out[0].DomainTags)
	}
	if out[1].PostID != 2 || out[1].Classification != "noise" {
		t.Errorf("geçersiz classification noise'a düşmeli: %+v", out[1])
	}
}

func TestParseClassifyResponseInvalid(t *testing.T) {
	if _, err := parseClassifyResponse("not json", []store.RawPost{{ID: 1}}); err == nil {
		t.Error("JSON olmayan yanıt hata dönmeli")
	}
	if _, err := parseClassifyResponse(`{"results":[]}`, []store.RawPost{{ID: 1}}); err == nil {
		t.Error("boş sonuç hata dönmeli")
	}
}
