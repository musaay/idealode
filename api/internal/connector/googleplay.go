package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// GooglePlay, TR pazarındaki sektör uygulamalarının kullanıcı yorumlarını
// çeker (Faz 1 rescope: TR-kaynaklı acılar). Resmi bir yorum API'si yoktur;
// Play Store web arayüzünün kullandığı batchexecute endpoint'i kullanılır
// (google-play-scraper kütüphanelerinin yaklaşımı — auth'suz, public).
//
// sources.community = uygulama paket kimliği (örn. "com.sahibinden"),
// sources.category  = sektör slug'ı (örn. "emlak").
// last_seen_ref     = görülen en yeni yorumun unix zamanı.
//
// Yalnızca <=3 yıldızlı yorumlar alınır: düşük yıldız = doğal acı filtresi;
// bu yüzden prefilter googleplay'i bypass eder.
type GooglePlay struct {
	BaseURL string // test için override edilebilir
}

// NewGooglePlay canlı Play Store endpoint'ine bağlı connector döner.
func NewGooglePlay() *GooglePlay {
	return &GooglePlay{BaseURL: "https://play.google.com"}
}

func (g *GooglePlay) Platform() string { return "googleplay" }

// Sayfa başına 100 yorum, koşu başına en fazla 2 sayfa ("sakin ilerleme").
const gpPageSize = 100
const gpMaxPages = 2
const gpMaxStars = 3

// gpReview, batchexecute cevabındaki tek yorumun ayrıştırılmış hali.
type gpReview struct {
	ID        string
	Author    string
	Text      string
	Rating    int
	ThumbsUp  int
	CreatedAt time.Time
}

func (g *GooglePlay) FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error) {
	since := sinceFromRef(src.LastSeenRef)
	client := newHTTPClient()

	var posts []store.RawPost
	maxSeen := since
	token := ""

	for page := 0; page < gpMaxPages; page++ {
		reviews, nextToken, err := g.fetchPage(ctx, client, src.Community, token)
		if err != nil {
			return nil, "", fmt.Errorf("googleplay: %w", err)
		}

		reachedSince := false
		for _, r := range reviews {
			ts := r.CreatedAt.Unix()
			if ts > maxSeen {
				maxSeen = ts
			}
			// sort=NEWEST: since'e ulaşınca gerisi eski demektir.
			if ts <= since {
				reachedSince = true
				continue
			}
			if r.Rating > gpMaxStars || strings.TrimSpace(r.Text) == "" {
				continue
			}
			posts = append(posts, store.RawPost{
				Platform:  g.Platform(),
				SourceRef: r.ID,
				Community: src.Community,
				Title:     fmt.Sprintf("Google Play yorumu — %s (%d★, %s)", src.Community, r.Rating, src.Category),
				Body:      r.Text,
				Author:    r.Author,
				URL:       "https://play.google.com/store/apps/details?id=" + src.Community + "&hl=tr",
				Score:     r.ThumbsUp,
				CreatedAt: r.CreatedAt.UTC(),
			})
		}

		if reachedSince || nextToken == "" || len(reviews) < gpPageSize {
			break
		}
		token = nextToken
	}

	newRef := ""
	if maxSeen > since {
		newRef = strconv.FormatInt(maxSeen, 10)
	}
	return posts, newRef, nil
}

// fetchPage, batchexecute'a tek istek atar ve yorumları + sonraki sayfa
// token'ını döner.
func (g *GooglePlay) fetchPage(ctx context.Context, client *http.Client, appID, token string) ([]gpReview, string, error) {
	endpoint := g.BaseURL + "/_/PlayStoreUi/data/batchexecute?hl=tr&gl=TR"
	body := gpRequestBody(appID, gpPageSize, token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return gpParseResponse(raw)
}

// gpRequestBody, batchexecute'un f.req form parametresini kurar. İç payload
// JSON-string-içinde-JSON olduğu için iki aşamalı marshal edilir.
func gpRequestBody(appID string, count int, token string) string {
	var tok any
	if token != "" {
		tok = token
	}
	// [null, null, [2, sort=2(NEWEST), [count, null, token], null, []], [appID, 7]]
	inner := []any{nil, nil, []any{2, 2, []any{count, nil, tok}, nil, []any{}}, []any{appID, 7}}
	innerJSON, _ := json.Marshal(inner)
	outer := []any{[]any{[]any{"UsvDTd", string(innerJSON), nil, "generic"}}}
	outerJSON, _ := json.Marshal(outer)
	return "f.req=" + url.QueryEscape(string(outerJSON))
}

// gpParseResponse, ")]}'" önekli batchexecute cevabını ayrıştırır.
// Zarf: [["wrb.fr","UsvDTd","<payload-json-string>",...],...]
// Payload: [[yorum,...], [null, "sonraki-sayfa-token"]]
// Yorum: [id, [yazar,...], yıldız, null, metin, [saniye,nano], beğeni, ...]
func gpParseResponse(raw []byte) ([]gpReview, string, error) {
	if i := bytes.IndexByte(raw, '['); i >= 0 {
		raw = raw[i:]
	}
	var env []json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil || len(env) == 0 {
		return nil, "", fmt.Errorf("zarf parse: %v", err)
	}
	var frame []json.RawMessage
	if err := json.Unmarshal(env[0], &frame); err != nil || len(frame) < 3 {
		return nil, "", fmt.Errorf("frame parse: %v", err)
	}
	var payloadStr string
	if err := json.Unmarshal(frame[2], &payloadStr); err != nil {
		return nil, "", fmt.Errorf("payload string parse: %v", err)
	}
	if payloadStr == "" {
		return nil, "", nil // yorum yok (örn. bilinmeyen uygulama)
	}
	var payload []json.RawMessage
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil || len(payload) == 0 {
		return nil, "", fmt.Errorf("payload parse: %v", err)
	}

	var rawReviews []json.RawMessage
	if err := json.Unmarshal(payload[0], &rawReviews); err != nil {
		return nil, "", fmt.Errorf("yorum listesi parse: %v", err)
	}

	var reviews []gpReview
	for _, rr := range rawReviews {
		if r, ok := gpParseReview(rr); ok {
			reviews = append(reviews, r)
		}
	}

	nextToken := ""
	if len(payload) > 1 {
		var tokenArr []json.RawMessage
		if json.Unmarshal(payload[1], &tokenArr) == nil && len(tokenArr) > 1 {
			_ = json.Unmarshal(tokenArr[1], &nextToken)
		}
	}
	return reviews, nextToken, nil
}

// gpParseReview tek yorumu ayrıştırır; şema dışı satırlar sessizce atlanır
// (endpoint resmi olmadığı için savunmacı davranılır).
func gpParseReview(raw json.RawMessage) (gpReview, bool) {
	var f []json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil || len(f) < 7 {
		return gpReview{}, false
	}
	var r gpReview
	if json.Unmarshal(f[0], &r.ID) != nil || r.ID == "" {
		return gpReview{}, false
	}
	var author []json.RawMessage
	if json.Unmarshal(f[1], &author) == nil && len(author) > 0 {
		_ = json.Unmarshal(author[0], &r.Author)
	}
	if json.Unmarshal(f[2], &r.Rating) != nil {
		return gpReview{}, false
	}
	_ = json.Unmarshal(f[4], &r.Text)
	var ts []int64
	if json.Unmarshal(f[5], &ts) != nil || len(ts) == 0 {
		return gpReview{}, false
	}
	r.CreatedAt = time.Unix(ts[0], 0)
	_ = json.Unmarshal(f[6], &r.ThumbsUp)
	return r, true
}
