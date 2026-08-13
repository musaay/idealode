package connector

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// StackExchange, api.stackexchange.com üzerinden seçili sitelerin yeni
// sorularını çeker. Geniş SE taraması YOK (Rev 2): softwarerecs çekirdek
// ("şu işi yapan araç var mı?" sorularının damıtılmış hâli), webapps ikincil.
//
// sources.community = site slug (softwarerecs, webapps).
// last_seen_ref = görülen en yeni sorunun creation_date unix zamanı.
type StackExchange struct {
	BaseURL string // test için override edilebilir
	AppKey  string // opsiyonel — quota'yı artırır (STACKEXCHANGE_KEY)
}

// NewStackExchange canlı API'ye bağlı connector döner.
func NewStackExchange(appKey string) *StackExchange {
	return &StackExchange{BaseURL: "https://api.stackexchange.com/2.3", AppKey: appKey}
}

func (s *StackExchange) Platform() string { return "stackexchange" }

// Koşu başına en fazla 5 sayfa x 100 soru ("sakin ilerleme").
const seMaxPages = 5

type seResponse struct {
	Items []struct {
		QuestionID   int64  `json:"question_id"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		Link         string `json:"link"`
		Score        int    `json:"score"`
		CreationDate int64  `json:"creation_date"`
		Owner        struct {
			DisplayName string `json:"display_name"`
		} `json:"owner"`
	} `json:"items"`
	HasMore        bool `json:"has_more"`
	Backoff        int  `json:"backoff"`
	QuotaRemaining int  `json:"quota_remaining"`
}

func (s *StackExchange) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	var posts []store.RawPost
	maxSeen := since

	for page := 1; page <= seMaxPages; page++ {
		q := url.Values{
			"order":    {"asc"},
			"sort":     {"creation"},
			"site":     {src.Community},
			"fromdate": {strconv.FormatInt(since+1, 10)},
			"pagesize": {"100"},
			"page":     {strconv.Itoa(page)},
			"filter":   {"withbody"},
		}
		if s.AppKey != "" {
			q.Set("key", s.AppKey)
		}

		var resp seResponse
		if err := getJSON(ctx, client, s.BaseURL+"/questions?"+q.Encode(), &resp); err != nil {
			return nil, "", fmt.Errorf("stackexchange (%s): %w", src.Community, err)
		}

		for _, item := range resp.Items {
			posts = append(posts, store.RawPost{
				Platform:  s.Platform(),
				SourceRef: fmt.Sprintf("%s-%d", src.Community, item.QuestionID),
				Community: src.Community,
				Title:     html.UnescapeString(item.Title),
				Body:      stripHTML(item.Body),
				Author:    item.Owner.DisplayName,
				URL:       item.Link,
				Score:     item.Score,
				CreatedAt: time.Unix(item.CreationDate, 0).UTC(),
			})
			if item.CreationDate > maxSeen {
				maxSeen = item.CreationDate
			}
		}

		// API'nin backoff alanına uyum — "sakin ilerleme" prensibi.
		if resp.Backoff > 0 {
			log.Printf("stackexchange: backoff %ds (site=%s)", resp.Backoff, src.Community)
			select {
			case <-time.After(time.Duration(min(resp.Backoff, 60)) * time.Second):
			case <-ctx.Done():
				return nil, "", ctx.Err()
			}
		}
		if !resp.HasMore {
			break
		}
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// stripHTML, SE'nin HTML gövdesini düz metne indirger (classification
// girdisi temiz ve ucuz kalsın diye).
func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
