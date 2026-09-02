// Package apiclient, `idealode serve` sürecinin `idealode api` sürecine
// konuştuğu HTTP istemcisidir. Web katmanı veritabanına DEĞİL bu istemciye
// bağlanır: Client, web.IdeaStore arayüzünü uygular.
//
// Hata sözleşmesi: kayıt yoksa store.ErrNotFound (web 404 render eder),
// diğer her şey (ağ hatası, 5xx, bozuk JSON) sarılı hata olarak döner ve
// web katmanında 502 sayfasına çevrilir.
package apiclient

import (
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

// maxBodyBytes, okunacak yanıt gövdesinin tavanı (bozuk/uçsuz yanıt koruması).
const maxBodyBytes = 4 << 20 // 4 MiB

// Client, API sürecine giden salt okunur istemci.
type Client struct {
	baseURL string
	http    *http.Client
}

// New, verilen taban adres ve istek zaman aşımıyla istemci kurar.
// baseURL sondaki "/" karakterlerinden arındırılır.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// ListIdeasFiltered, `GET /api/ideas` — filtreli kart listesi.
func (c *Client) ListIdeasFiltered(ctx context.Context, f store.IdeaFilter) ([]store.Idea, error) {
	q := url.Values{}
	if f.SourceType != "" {
		q.Set("source_type", f.SourceType)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}

	var body struct {
		Ideas []store.Idea `json:"ideas"`
	}
	if err := c.get(ctx, "/api/ideas", q, &body); err != nil {
		return nil, err
	}
	if body.Ideas == nil {
		// nil slice tuzağı: şablon tarafına her zaman boş dilim gider.
		return []store.Idea{}, nil
	}
	return body.Ideas, nil
}

// GetIdea, `GET /api/ideas/{id}` — tek kart; yoksa store.ErrNotFound.
func (c *Client) GetIdea(ctx context.Context, id int64) (*store.Idea, error) {
	var body struct {
		Idea *store.Idea `json:"idea"`
	}
	path := "/api/ideas/" + strconv.FormatInt(id, 10)
	if err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	if body.Idea == nil {
		// 200 döndü ama gövdede kart yok — sözleşme ihlali, 404 değil.
		return nil, fmt.Errorf("api yanıtında kart alanı yok (%s)", path)
	}
	return body.Idea, nil
}

// sourceDTO, IdeaSource'un tel üzerindeki biçimi. store.IdeaSource'un kendi
// etiketlerine bağlanmak yerine sözleşme burada birebir sabitlenir.
type sourceDTO struct {
	Platform  string    `json:"platform"`
	Community string    `json:"community"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// IdeaSources, `GET /api/ideas/{id}/sources` — kartın kaynak gönderileri.
func (c *Client) IdeaSources(ctx context.Context, ideaID int64) ([]store.IdeaSource, error) {
	var body struct {
		Sources []sourceDTO `json:"sources"`
	}
	path := "/api/ideas/" + strconv.FormatInt(ideaID, 10) + "/sources"
	if err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	out := make([]store.IdeaSource, 0, len(body.Sources))
	for _, s := range body.Sources {
		out = append(out, store.IdeaSource{
			Platform:  s.Platform,
			Community: s.Community,
			URL:       s.URL,
			CreatedAt: s.CreatedAt,
		})
	}
	return out, nil
}

// get, tek GET isteğini yapar ve JSON gövdesini out'a çözer.
// 404 → store.ErrNotFound (sarılı; errors.Is çalışır).
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("api isteği hazırlanamadı (%s): %w", path, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// ctx iptali/zaman aşımı buraya düşer; sarmalanarak korunur.
		return fmt.Errorf("api'ye ulaşılamadı (%s): %w", path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("api (%s): %w", path, store.ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("api beklenmeyen durum (%s): %d", path, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("api yanıtı okunamadı (%s): %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("api yanıtı çözümlenemedi (%s): %w", path, err)
	}
	return nil
}
