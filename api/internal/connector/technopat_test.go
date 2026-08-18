package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/musaay/idealode/api/internal/store"
)

const tpTestFeed = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel>
<title>Technopat Sosyal</title>
<item>
<title>Fatura takibi için uygulama var mı?</title>
<pubDate>Tue, 14 Nov 2023 22:22:00 +0000</pubDate>
<link>https://www.technopat.net/sosyal/konu/fatura-takibi.999/</link>
<dc:creator>mehmet</dc:creator>
<description><![CDATA[<div>Aylık faturaları tek yerden takip eden <b>bir uygulama</b> arıyorum, öneriniz var mı?</div>]]></description>
</item>
<item>
<title>Eski konu</title>
<pubDate>Tue, 14 Nov 2023 21:00:00 +0000</pubDate>
<link>https://www.technopat.net/sosyal/konu/eski.998/</link>
<dc:creator>ali</dc:creator>
<description>eski içerik</description>
</item>
</channel>
</rss>`

func TestTechnopatFetchNew(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(tpTestFeed))
	}))
	defer srv.Close()

	tp := &Technopat{BaseURL: srv.URL}
	// since = 1699995600 (21:00 ile 22:22 arası) — yalnız yeni konu gelmeli.
	src := store.Source{Platform: "technopat", Community: "-", LastSeenRef: "1699996800"}

	posts, newRef, err := tp.FetchNew(context.Background(), src)
	if err != nil {
		t.Fatalf("FetchNew: %v", err)
	}
	if gotPath != "/sosyal/forums/-/index.rss" {
		t.Errorf("yanlış feed path: %q", gotPath)
	}
	if len(posts) != 1 {
		t.Fatalf("1 post beklenirdi, geldi: %d", len(posts))
	}
	p := posts[0]
	if p.Title != "Fatura takibi için uygulama var mı?" || p.Author != "mehmet" {
		t.Errorf("yanlış post: %+v", p)
	}
	if p.Body != "Aylık faturaları tek yerden takip eden bir uygulama arıyorum, öneriniz var mı?" {
		t.Errorf("gövde HTML'den arındırılmalı: %q", p.Body)
	}
	// 14 Kas 2023 22:22:00 UTC = 1700000520
	if newRef != "1700000520" {
		t.Errorf("newRef 1700000520 olmalı, geldi: %q", newRef)
	}
}
