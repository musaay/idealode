package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

// gpTestResponse, batchexecute cevabını üretir: verilen yorumlar + token.
func gpTestResponse(t *testing.T, reviews []any, token string) string {
	t.Helper()
	payload, err := json.Marshal([]any{reviews, []any{nil, token}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := json.Marshal([]any{[]any{"wrb.fr", "UsvDTd", string(payload), nil, nil, nil, "generic"}})
	if err != nil {
		t.Fatal(err)
	}
	return ")]}'\n\n" + string(env)
}

func gpTestReview(id, author, text string, rating int, ts int64) []any {
	return []any{id, []any{author, []any{}}, rating, nil, text, []any{ts, 0}, 3, nil, nil}
}

func TestGooglePlayFetchNew(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		reviews := []any{
			gpTestReview("rev-yeni", "Ali", "Uygulama sürekli çöküyor, ilan filtreleri kayboluyor", 2, 1700000500),
			gpTestReview("rev-mutlu", "Veli", "Harika uygulama", 5, 1700000400),
			gpTestReview("rev-eski", "Ayşe", "Eski şikayet", 1, 1699999000),
		}
		w.Write([]byte(gpTestResponse(t, reviews, "")))
	}))
	defer srv.Close()

	gp := &GooglePlay{BaseURL: srv.URL}
	src := store.Source{Platform: "googleplay", Community: "com.sahibinden", Category: "emlak", LastSeenRef: "1700000000"}

	posts, newRef, err := gp.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}

	if !strings.Contains(gotBody, "f.req=") {
		t.Errorf("istek f.req içermeli, geldi: %q", truncate(gotBody, 80))
	}
	if !strings.Contains(gotBody, "com.sahibinden") {
		t.Errorf("istek paket kimliğini içermeli")
	}

	// 5 yıldızlı ve since'ten eski yorumlar elenir; tek post kalır.
	if len(posts) != 1 {
		t.Fatalf("1 post beklenirdi, geldi: %d", len(posts))
	}
	p := posts[0]
	if p.SourceRef != "rev-yeni" || p.Author != "Ali" {
		t.Errorf("yanlış post: %+v", p)
	}
	if !strings.Contains(p.Title, "2★") || !strings.Contains(p.Title, "emlak") {
		t.Errorf("başlık yıldız ve sektör içermeli: %q", p.Title)
	}
	if p.Body != "Uygulama sürekli çöküyor, ilan filtreleri kayboluyor" {
		t.Errorf("gövde yorumun metni olmalı: %q", p.Body)
	}
	if newRef != "1700000500" {
		t.Errorf("newRef 1700000500 olmalı, geldi: %q", newRef)
	}
}

func TestGooglePlayEmptyPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env, _ := json.Marshal([]any{[]any{"wrb.fr", "UsvDTd", "", nil, nil, nil, "generic"}})
		w.Write([]byte(")]}'\n\n" + string(env)))
	}))
	defer srv.Close()

	gp := &GooglePlay{BaseURL: srv.URL}
	posts, newRef, err := gp.FetchNew(context.Background(), store.Source{Community: "com.yok", LastSeenRef: "1700000000"})
	if err != nil {
		t.Fatalf("boş payload hata üretmemeli: %v", err)
	}
	if len(posts) != 0 || newRef != "" {
		t.Errorf("boş sonuç beklenirdi: posts=%d newRef=%q", len(posts), newRef)
	}
}
