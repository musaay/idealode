package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// Technopat, Türkiye'nin büyük teknoloji forumunun (XenForo) public RSS
// akışından yeni konuları çeker (Faz 1 rescope: TR-kaynaklı acılar).
//
// sources.community = forum slug'ı ("-" = tüm forumların birleşik akışı).
// last_seen_ref     = görülen en yeni konunun unix zamanı.
type Technopat struct {
	BaseURL string // test için override edilebilir
}

// NewTechnopat canlı technopat.net'e bağlı connector döner.
func NewTechnopat() *Technopat {
	return &Technopat{BaseURL: "https://www.technopat.net"}
}

func (t *Technopat) Platform() string { return "technopat" }

type tpFeed struct {
	Channel struct {
		Items []tpItem `xml:"item"`
	} `xml:"channel"`
}

type tpItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	PubDate     string `xml:"pubDate"`
	Creator     string `xml:"creator"`
	Description string `xml:"description"`
}

func (t *Technopat) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	feedURL := fmt.Sprintf("%s/sosyal/forums/%s/index.rss", t.BaseURL, src.Community)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("technopat: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("technopat: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var feed tpFeed
	if err := xml.Unmarshal(raw, &feed); err != nil {
		return nil, "", fmt.Errorf("technopat: rss parse: %w", err)
	}

	var posts []store.RawPost
	maxSeen := since

	for _, item := range feed.Channel.Items {
		created, err := time.Parse(time.RFC1123Z, item.PubDate)
		if err != nil {
			continue
		}
		ts := created.Unix()
		if ts > maxSeen {
			maxSeen = ts
		}
		if ts <= since || item.Link == "" {
			continue
		}
		posts = append(posts, store.RawPost{
			Platform:  t.Platform(),
			SourceRef: item.Link,
			Community: src.Community,
			Title:     item.Title,
			Body:      stripHTML(item.Description),
			Author:    item.Creator,
			URL:       item.Link,
			Score:     0,
			CreatedAt: created.UTC(),
		})
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}
