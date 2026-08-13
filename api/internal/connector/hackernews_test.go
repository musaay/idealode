package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

func TestHackerNewsFetchNew(t *testing.T) {
	var gotQuery, gotFilters string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotFilters = r.URL.Query().Get("numericFilters")
		json.NewEncoder(w).Encode(map[string]any{
			"nbPages": 1,
			"hits": []map[string]any{
				{
					"objectID": "101", "title": "Is there a tool for X?", "url": "https://example.com/x",
					"author": "alice", "points": 42, "story_text": "I keep doing X by hand.",
					"created_at_i": 1700000100, "_tags": []string{"story"},
				},
				{
					"objectID": "102", "comment_text": "I wish there was a tool for Y",
					"story_title": "Ask HN: workflows", "author": "bob",
					"created_at_i": 1700000200, "_tags": []string{"comment"},
				},
			},
		})
	}))
	defer srv.Close()

	hn := &HackerNews{BaseURL: srv.URL}
	src := store.Source{Platform: "hackernews", Community: "is there a tool", LastSeenRef: "1700000000"}

	posts, newRef, err := hn.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}

	if gotQuery != `"is there a tool"` {
		t.Errorf("sorgu tırnaklanmalı (phrase search), geldi: %q", gotQuery)
	}
	if gotFilters != "created_at_i>1700000000" {
		t.Errorf("tarih filtresi last_seen_ref'ten gelmeli, geldi: %q", gotFilters)
	}
	if len(posts) != 2 {
		t.Fatalf("2 post beklendi, geldi: %d", len(posts))
	}

	story := posts[0]
	if story.SourceRef != "101" || story.Title != "Is there a tool for X?" ||
		story.Body != "I keep doing X by hand." || story.URL != "https://example.com/x" || story.Score != 42 {
		t.Errorf("story mapping hatalı: %+v", story)
	}

	comment := posts[1]
	if comment.SourceRef != "102" || comment.Title != "Ask HN: workflows" ||
		comment.Body != "I wish there was a tool for Y" ||
		comment.URL != "https://news.ycombinator.com/item?id=102" {
		t.Errorf("comment mapping hatalı: %+v", comment)
	}

	if newRef != "1700000200" {
		t.Errorf("newRef en yeni created_at_i olmalı, geldi: %q", newRef)
	}
}

func TestHackerNewsNoNewItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"nbPages": 0, "hits": []any{}})
	}))
	defer srv.Close()

	hn := &HackerNews{BaseURL: srv.URL}
	posts, newRef, err := hn.FetchNew(context.Background(), store.Source{Community: "x", LastSeenRef: "1700000000"})
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if len(posts) != 0 || newRef != "" {
		t.Errorf("yeni item yokken boş sonuç + boş ref beklenir; geldi: %d post, ref=%q", len(posts), newRef)
	}
}
