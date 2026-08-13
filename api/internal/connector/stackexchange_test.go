package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

func TestStackExchangeFetchNew(t *testing.T) {
	var gotSite, gotFromdate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSite = r.URL.Query().Get("site")
		gotFromdate = r.URL.Query().Get("fromdate")
		json.NewEncoder(w).Encode(map[string]any{
			"has_more": false,
			"items": []map[string]any{
				{
					"question_id": 7001,
					"title":       "Tool to convert &quot;X&quot; to Y?",
					"body":        "<p>I need a <b>free</b> tool that converts X to Y.</p>",
					"link":        "https://softwarerecs.stackexchange.com/q/7001",
					"score":       3,
					"creation_date": 1700000500,
					"owner":       map[string]any{"display_name": "carol"},
				},
			},
		})
	}))
	defer srv.Close()

	se := &StackExchange{BaseURL: srv.URL}
	src := store.Source{Platform: "stackexchange", Community: "softwarerecs", LastSeenRef: "1700000000"}

	posts, newRef, err := se.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}

	if gotSite != "softwarerecs" {
		t.Errorf("site parametresi community'den gelmeli, geldi: %q", gotSite)
	}
	if gotFromdate != "1700000001" {
		t.Errorf("fromdate last_seen+1 olmalı, geldi: %q", gotFromdate)
	}
	if len(posts) != 1 {
		t.Fatalf("1 post beklendi, geldi: %d", len(posts))
	}

	p := posts[0]
	if p.SourceRef != "softwarerecs-7001" {
		t.Errorf("source_ref site-qid formatında olmalı, geldi: %q", p.SourceRef)
	}
	if p.Title != `Tool to convert "X" to Y?` {
		t.Errorf("title entity'leri çözülmeli, geldi: %q", p.Title)
	}
	if p.Body != "I need a free tool that converts X to Y." {
		t.Errorf("body HTML'den arındırılmalı, geldi: %q", p.Body)
	}
	if newRef != "1700000500" {
		t.Errorf("newRef creation_date olmalı, geldi: %q", newRef)
	}
}

func TestStripHTML(t *testing.T) {
	in := "<p>Hello <a href='x'>world</a>,&nbsp;&amp; more</p>\n\n<pre>code</pre>"
	want := "Hello world , & more code"
	if got := stripHTML(in); got != want {
		t.Errorf("stripHTML: %q, beklenen %q", got, want)
	}
}
