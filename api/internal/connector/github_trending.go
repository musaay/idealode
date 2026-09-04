package connector

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// GitHubTrending, github.com/trending sayfasından günlük yükselen repoları
// çeker (#50 B parçası — "GitHub ivme" kanıtı).
//
// last_seen_ref KULLANILMAZ: gün-kapısı source_ref içinde
// ("owner/repo:YYYY-MM-DD") tutulur, aynı gün tekrar koşu ON CONFLICT DO
// NOTHING ile yutulur. Sayfa her koşuda tam çekilir (tek istek, sakin
// ilerleme zaten sağlanmış olur).
type GitHubTrending struct {
	BaseURL string // test için override edilebilir
}

// NewGitHubTrending, canlı github.com'a bağlı connector döner.
func NewGitHubTrending() *GitHubTrending {
	return &GitHubTrending{BaseURL: "https://github.com"}
}

func (g *GitHubTrending) Platform() string { return "github_trending" }

// articleRe: her repo satırını (<article class="Box-row">...</article>)
// ayırır. Sayfa yapısında iç içe <article> yok, non-greedy eşleşme güvenli.
var articleRe = regexp.MustCompile(`(?s)<article class="Box-row">.*?</article>`)

// repoHrefRe: repo başlığı bağlantısındaki owner/repo yolunu yakalar.
var repoHrefRe = regexp.MustCompile(`(?s)<h2 class="h3 lh-condensed">.*?href="/([^"/]+/[^"/]+)"`)

// descRe: repo açıklaması (opsiyonel — bazı repoların açıklaması olmayabilir).
var descRe = regexp.MustCompile(`(?s)<p class="col-9 color-fg-muted my-1[^"]*">(.*?)</p>`)

// totalStarsRe: toplam yıldız sayısı (stargazers linki, binlik virgüllü).
var totalStarsRe = regexp.MustCompile(`(?s)stargazers"[^>]*>.*?([\d,]+)\s*</a>`)

// dailyStarsRe: "N stars today" / "N stars this week" biçimindeki günlük artış.
var dailyStarsRe = regexp.MustCompile(`([\d,]+)\s+stars? today`)

// FetchNew, trending sayfasını tek seferde çeker ve her repo için bir
// RawPost üretir. 0 repo parse edilmesi HTML yapısının değiştiğinin
// sinyalidir — hata döner (ingest.go bunu HATA olarak loglar, diğer
// kaynaklar etkilenmez).
func (g *GitHubTrending) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	client := newHTTPClient()

	url := g.BaseURL + "/trending?since=daily"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("github_trending: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("github_trending: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	articles := articleRe.FindAllString(string(raw), -1)

	today := time.Now().UTC().Truncate(24 * time.Hour)
	dateStr := today.Format("2006-01-02")

	var posts []store.RawPost
	for _, art := range articles {
		hrefMatch := repoHrefRe.FindStringSubmatch(art)
		if hrefMatch == nil {
			continue
		}
		ownerRepo := hrefMatch[1]

		desc := ""
		if m := descRe.FindStringSubmatch(art); m != nil {
			desc = stripHTML(m[1])
		}

		totalStars := ""
		if m := totalStarsRe.FindStringSubmatch(art); m != nil {
			totalStars = strings.TrimSpace(m[1])
		}

		daily := 0
		if m := dailyStarsRe.FindStringSubmatch(art); m != nil {
			daily = parseThousands(m[1])
		}

		body := desc
		if totalStars != "" {
			body = "★" + totalStars + " · " + desc
		}
		body = html.UnescapeString(strings.TrimSpace(body))

		posts = append(posts, store.RawPost{
			Platform:  g.Platform(),
			SourceRef: ownerRepo + ":" + dateStr,
			Community: "daily",
			Title:     ownerRepo,
			Body:      body,
			Author:    strings.SplitN(ownerRepo, "/", 2)[0],
			URL:       g.BaseURL + "/" + ownerRepo,
			Score:     daily,
			CreatedAt: today,
		})
	}

	if len(posts) == 0 {
		return nil, "", fmt.Errorf("github_trending: 0 repo parse edildi — HTML yapısı değişmiş olabilir")
	}

	// last_seen_ref kullanılmaz (gün-kapısı source_ref içinde).
	return posts, "", nil
}

// parseThousands, "25,222" gibi binlik virgüllü sayıyı int'e çevirir;
// ayrıştırılamazsa 0 döner.
func parseThousands(s string) int {
	n, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	if err != nil {
		return 0
	}
	return n
}
