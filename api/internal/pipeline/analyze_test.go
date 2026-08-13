package pipeline

import (
	"reflect"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

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
