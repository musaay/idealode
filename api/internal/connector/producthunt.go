package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// ProductHunt, topic bazlı yeni lansmanları ve en oylanan yorumlarını çeker
// (GraphQL API v2, developer token zorunlu — token yoksa connector registry'ye
// hiç girmez, ingest "connector yok" logu ile kaynağı atlar).
//
// sources.community = topic slug (örn. "developer-tools").
// last_seen_ref     = görülen en yeni lansmanın unix zamanı.
//
// Yorumlar ayrı raw_posts satırları olur: "I wish it also did X" tarzı
// boşluk sinyalleri lansmanın kendisinden çok yorumlarda yaşar.
type ProductHunt struct {
	BaseURL string // test için override edilebilir
	Token   string
}

// NewProductHunt canlı Product Hunt API'sine bağlı connector döner.
func NewProductHunt(token string) *ProductHunt {
	return &ProductHunt{BaseURL: "https://api.producthunt.com", Token: token}
}

func (ph *ProductHunt) Platform() string { return "producthunt" }

// Koşu başına topic başına en fazla 20 lansman, lansman başına 3 yorum
// ("sakin ilerleme").
const phPostsPerFetch = 20
const phCommentsPerPost = 3

const phQuery = `query($topic: String!, $postedAfter: DateTime!) {
  posts(first: 20, topic: $topic, postedAfter: $postedAfter, order: NEWEST) {
    edges { node {
      id name tagline description url votesCount createdAt
      user { username }
      comments(first: 3) { edges { node { id body createdAt votesCount user { username } } } }
    } }
  }
}`

type phResponse struct {
	Data struct {
		Posts struct {
			Edges []struct {
				Node struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Tagline     string `json:"tagline"`
					Description string `json:"description"`
					URL         string `json:"url"`
					VotesCount  int    `json:"votesCount"`
					CreatedAt   string `json:"createdAt"`
					User        struct {
						Username string `json:"username"`
					} `json:"user"`
					Comments struct {
						Edges []struct {
							Node struct {
								ID         string `json:"id"`
								Body       string `json:"body"`
								CreatedAt  string `json:"createdAt"`
								VotesCount int    `json:"votesCount"`
								User       struct {
									Username string `json:"username"`
								} `json:"user"`
							} `json:"node"`
						} `json:"edges"`
					} `json:"comments"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"posts"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (ph *ProductHunt) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	payload, err := json.Marshal(map[string]any{
		"query": phQuery,
		"variables": map[string]any{
			"topic":       src.Community,
			"postedAfter": time.Unix(since, 0).UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ph.BaseURL+"/v2/api/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ph.Token)

	var resp phResponse
	if err := doJSON(client, req, &resp); err != nil {
		return nil, "", fmt.Errorf("producthunt: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, "", fmt.Errorf("producthunt graphql: %s", resp.Errors[0].Message)
	}

	var posts []store.RawPost
	maxSeen := since

	for _, edge := range resp.Data.Posts.Edges {
		n := edge.Node
		created, err := time.Parse(time.RFC3339, n.CreatedAt)
		if err != nil {
			continue
		}
		ts := created.Unix()
		if ts > maxSeen {
			maxSeen = ts
		}
		if ts <= since {
			continue
		}

		body := n.Tagline
		if n.Description != "" {
			body = n.Tagline + "\n\n" + n.Description
		}
		posts = append(posts, store.RawPost{
			Platform:  ph.Platform(),
			SourceRef: n.ID,
			Community: src.Community,
			Title:     n.Name,
			Body:      body,
			Author:    n.User.Username,
			URL:       n.URL,
			Score:     n.VotesCount,
			CreatedAt: created.UTC(),
		})

		for _, ce := range n.Comments.Edges {
			c := ce.Node
			cCreated, err := time.Parse(time.RFC3339, c.CreatedAt)
			if err != nil || c.Body == "" {
				continue
			}
			posts = append(posts, store.RawPost{
				Platform:  ph.Platform(),
				SourceRef: "comment-" + c.ID,
				Community: src.Community,
				Title:     n.Name + " — kullanıcı yorumu",
				Body:      c.Body,
				Author:    c.User.Username,
				URL:       n.URL,
				Score:     c.VotesCount,
				CreatedAt: cCreated.UTC(),
			})
		}
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}
