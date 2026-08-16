package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

func TestGitHubFetchNew(t *testing.T) {
	var gotQ, gotAdvanced, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query().Get("q")
		gotAdvanced = r.URL.Query().Get("advanced_search")
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 2,
			"items": []map[string]any{
				{
					"id": 501, "title": "Feature: dark mode", "body": "I wish there was a dark mode",
					"html_url": "https://github.com/a/b/issues/1", "comments": 7,
					"created_at": "2023-11-14T22:15:00Z", "user": map[string]any{"login": "alice"},
				},
				{
					"id": 502, "title": "Export to CSV", "body": "",
					"html_url": "https://github.com/c/d/issues/9", "comments": 0,
					"created_at": "2023-11-14T22:20:00Z", "user": map[string]any{"login": "bob"},
				},
			},
		})
	}))
	defer srv.Close()

	gh := &GitHub{BaseURL: srv.URL, Token: "tkn"}
	src := store.Source{Platform: "github", Community: `"i wish" in:body is:issue`, LastSeenRef: "1700000000"}

	posts, newRef, err := gh.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}

	if gotAdvanced != "true" {
		t.Errorf("advanced_search=true zorunlu (Mart 2025 API değişikliği), geldi: %q", gotAdvanced)
	}
	if gotQ != `"i wish" in:body is:issue created:>2023-11-14T22:13:20Z` {
		t.Errorf("query, community + created filtresi olmalı; geldi: %q", gotQ)
	}
	if gotAuth != "Bearer tkn" {
		t.Errorf("token verilmişse Bearer başlığı gitmeli, geldi: %q", gotAuth)
	}
	if len(posts) != 2 {
		t.Fatalf("2 post beklendi, geldi: %d", len(posts))
	}

	p := posts[0]
	if p.SourceRef != "501" || p.Title != "Feature: dark mode" ||
		p.Body != "I wish there was a dark mode" || p.URL != "https://github.com/a/b/issues/1" ||
		p.Author != "alice" || p.Score != 7 {
		t.Errorf("issue mapping hatalı: %+v", p)
	}

	// 2023-11-14T22:20:00Z = 1700000400 — newRef en yeni created_at unix'i.
	if newRef != "1700000400" {
		t.Errorf("newRef en yeni created_at olmalı, geldi: %q", newRef)
	}
}

func TestGitHubAddsIsIssue(t *testing.T) {
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query().Get("q")
		json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "items": []any{}})
	}))
	defer srv.Close()

	gh := &GitHub{BaseURL: srv.URL}
	src := store.Source{Platform: "github", Community: `label:"feature request" state:open`, LastSeenRef: "1700000000"}

	posts, newRef, err := gh.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if len(posts) != 0 || newRef != "" {
		t.Errorf("yeni item yokken boş sonuç + boş ref beklenir; geldi: %d post, ref=%q", len(posts), newRef)
	}
	want := `label:"feature request" state:open is:issue created:>2023-11-14T22:13:20Z`
	if gotQ != want {
		t.Errorf("tip belirtilmemiş sorguya is:issue eklenmeli;\nbeklenen: %q\ngelen:    %q", want, gotQ)
	}
}
