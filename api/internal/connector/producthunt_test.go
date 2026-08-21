package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

const phTestResponse = `{"data":{"posts":{"edges":[
  {"node":{"id":"p1","name":"CoolTool","tagline":"Does cool things","description":"Longer desc",
    "url":"https://ph.example/p1","votesCount":42,"createdAt":"2023-11-15T10:00:00Z",
    "user":{"username":"maker1"},
    "comments":{"edges":[
      {"node":{"id":"c1","body":"I wish it also did X","createdAt":"2023-11-15T11:00:00Z","votesCount":5,"user":{"username":"fan1"}}}
    ]}}},
  {"node":{"id":"p0","name":"OldTool","tagline":"old","description":"",
    "url":"https://ph.example/p0","votesCount":1,"createdAt":"2023-11-13T10:00:00Z",
    "user":{"username":"maker0"},"comments":{"edges":[]}}}
]}}}`

func TestProductHuntFetchNew(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(phTestResponse))
	}))
	defer srv.Close()

	ph := &ProductHunt{BaseURL: srv.URL, Token: "tok123"}
	// since = 14 Kas 2023 → OldTool (13 Kas) elenir, CoolTool + yorumu gelir.
	src := store.Source{Platform: "producthunt", Community: "developer-tools", LastSeenRef: "1699920000"}

	posts, newRef, err := ph.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Bearer token bekleniyordu: %q", gotAuth)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("istek JSON değil: %v", err)
	}
	vars := req["variables"].(map[string]any)
	if vars["topic"] != "developer-tools" {
		t.Errorf("topic değişkeni yanlış: %v", vars["topic"])
	}

	if len(posts) != 2 {
		t.Fatalf("2 post beklenirdi (lansman + yorum), geldi: %d", len(posts))
	}
	launch, comment := posts[0], posts[1]
	if launch.SourceRef != "p1" || launch.Score != 42 || !strings.Contains(launch.Body, "Does cool things") {
		t.Errorf("lansman post'u yanlış: %+v", launch)
	}
	if comment.SourceRef != "comment-c1" || comment.Body != "I wish it also did X" {
		t.Errorf("yorum post'u yanlış: %+v", comment)
	}
	// CoolTool createdAt 2023-11-15T10:00:00Z = 1700042400
	if newRef != "1700042400" {
		t.Errorf("newRef 1700042400 olmalı, geldi: %q", newRef)
	}
}

func TestProductHuntGraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"invalid token"}]}`))
	}))
	defer srv.Close()

	ph := &ProductHunt{BaseURL: srv.URL, Token: "bad"}
	if _, _, err := ph.FetchNew(context.Background(), store.Source{Community: "saas"}); err == nil {
		t.Error("graphql hatası error dönmeli")
	}
}
