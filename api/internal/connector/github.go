package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// GitHub, search/issues API ile TÜM public repo'larda anahtar kelime taraması
// yapar. Mart 2025 API değişikliği gereği çağrılar advanced_search=true
// parametresiyle yapılır.
//
// sources.community = search query (örn. `"i wish" in:body is:issue`).
// last_seen_ref = görülen en yeni issue'nun created_at unix zamanı.
// Token opsiyoneldir (GITHUB_TOKEN): 60/saat -> 5000/saat; search ayrıca
// 30/dk ile sınırlıdır — sayfa sayısı düşük tutulur ("sakin ilerleme").
type GitHub struct {
	BaseURL string // test için override edilebilir
	Token   string // opsiyonel PAT
}

// NewGitHub canlı GitHub API'sine bağlı connector döner.
func NewGitHub(token string) *GitHub {
	return &GitHub{BaseURL: "https://api.github.com", Token: token}
}

func (g *GitHub) Platform() string { return "github" }

// Sayfa başına 100 sonuç; koşu başına en fazla 2 sayfa — anonim search
// limiti 10 istek/dk, kaynak başına 2 istek bunun altında kalır.
const ghPerPage = 100
const ghMaxPages = 2

type ghResponse struct {
	Items []struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		HTMLURL   string `json:"html_url"`
		Comments  int    `json:"comments"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"items"`
	TotalCount int `json:"total_count"`
}

func (g *GitHub) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	// Tarih filtresi query qualifier'ı olarak eklenir; PR'lar dışarıda
	// kalsın diye sorguda tip belirtilmemişse is:issue eklenir.
	query := src.Community
	if !strings.Contains(query, "is:issue") && !strings.Contains(query, "is:pull-request") {
		query += " is:issue"
	}
	query += " created:>" + time.Unix(since, 0).UTC().Format("2006-01-02T15:04:05Z")

	var posts []store.RawPost
	maxSeen := since

	for page := 1; page <= ghMaxPages; page++ {
		q := url.Values{
			"q":               {query},
			"advanced_search": {"true"},
			"sort":            {"created"},
			"order":           {"asc"},
			"per_page":        {strconv.Itoa(ghPerPage)},
			"page":            {strconv.Itoa(page)},
		}

		var resp ghResponse
		if err := g.getJSON(ctx, client, g.BaseURL+"/search/issues?"+q.Encode(), &resp); err != nil {
			return nil, "", fmt.Errorf("github (%q): %w", src.Community, err)
		}

		for _, item := range resp.Items {
			created, err := time.Parse(time.RFC3339, item.CreatedAt)
			if err != nil {
				continue
			}
			posts = append(posts, store.RawPost{
				Platform:  g.Platform(),
				SourceRef: strconv.FormatInt(item.ID, 10),
				Community: src.Community,
				Title:     item.Title,
				Body:      item.Body,
				Author:    item.User.Login,
				URL:       item.HTMLURL,
				Score:     item.Comments,
				CreatedAt: created.UTC(),
			})
			if u := created.Unix(); u > maxSeen {
				maxSeen = u
			}
		}

		if len(resp.Items) < ghPerPage {
			break
		}
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}

// getJSON, ortak getJSON'un Authorization başlıklı biçimi.
func (g *GitHub) getJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	return doJSON(client, req, target)
}
