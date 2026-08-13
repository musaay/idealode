package connector

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// HackerNews, keyword aramasını Algolia HN Search API ile yapar
// (hn.algolia.com/api — ücretsiz, auth'suz, tam metin arama + tarih filtresi).
// Resmi Firebase API'de arama YOK; Firebase yalnızca gerekirse item detayı
// için yedektir (V1'de kullanılmıyor).
//
// sources.community = kayıtlı arama sorgusu (örn. "is there a tool").
// last_seen_ref = görülen en yeni item'ın created_at_i unix zamanı.
type HackerNews struct {
	BaseURL string // test için override edilebilir
}

// NewHackerNews canlı Algolia API'sine bağlı connector döner.
func NewHackerNews() *HackerNews {
	return &HackerNews{BaseURL: "https://hn.algolia.com/api/v1"}
}

func (h *HackerNews) Platform() string { return "hackernews" }

// Sayfa başına 100 sonuç; koşu başına en fazla 3 sayfa ("sakin ilerleme").
const hnHitsPerPage = 100
const hnMaxPages = 3

type hnResponse struct {
	Hits []struct {
		ObjectID    string   `json:"objectID"`
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Author      string   `json:"author"`
		Points      int      `json:"points"`
		StoryText   string   `json:"story_text"`
		CommentText string   `json:"comment_text"`
		StoryTitle  string   `json:"story_title"`
		CreatedAtI  int64    `json:"created_at_i"`
		Tags        []string `json:"_tags"`
	} `json:"hits"`
	NbPages int `json:"nbPages"`
}

func (h *HackerNews) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	// Sorgu tam kalıp (phrase) olarak aransın diye tırnaklanır;
	// advancedSyntax=true tırnaklı aramayı etkinleştirir.
	query := src.Community
	if !strings.HasPrefix(query, `"`) {
		query = `"` + query + `"`
	}

	var posts []store.RawPost
	maxSeen := since

	for page := 0; page < hnMaxPages; page++ {
		u := fmt.Sprintf(
			"%s/search_by_date?query=%s&tags=(story,comment)&advancedSyntax=true&hitsPerPage=%d&numericFilters=%s&page=%d",
			h.BaseURL, url.QueryEscape(query), hnHitsPerPage,
			url.QueryEscape(fmt.Sprintf("created_at_i>%d", since)), page,
		)

		var resp hnResponse
		if err := getJSON(ctx, client, u, &resp); err != nil {
			return nil, "", fmt.Errorf("algolia: %w", err)
		}

		for _, hit := range resp.Hits {
			p := store.RawPost{
				Platform:  h.Platform(),
				SourceRef: hit.ObjectID,
				Community: src.Community,
				Author:    hit.Author,
				Score:     hit.Points,
				CreatedAt: time.Unix(hit.CreatedAtI, 0).UTC(),
			}
			itemURL := "https://news.ycombinator.com/item?id=" + hit.ObjectID
			if contains(hit.Tags, "comment") {
				// Yorum: başlık üst story'den, gövde yorum metni.
				p.Title = hit.StoryTitle
				p.Body = hit.CommentText
				p.URL = itemURL
			} else {
				p.Title = hit.Title
				p.Body = hit.StoryText
				if hit.URL != "" {
					p.URL = hit.URL
				} else {
					p.URL = itemURL
				}
			}
			if hit.CreatedAtI > maxSeen {
				maxSeen = hit.CreatedAtI
			}
			posts = append(posts, p)
		}

		if len(resp.Hits) < hnHitsPerPage || page+1 >= resp.NbPages {
			break
		}
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}

// sinceFromRef, last_seen_ref'i unix zamanına çevirir; yok/bozuksa ilk çekim
// penceresi (şimdi - initialWindow) kullanılır.
func sinceFromRef(ref string) int64 {
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil && n > 0 {
		return n
	}
	return time.Now().Add(-initialWindow).Unix()
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
