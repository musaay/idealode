// Package apiclient, `idealode serve` sürecinin `idealode api` sürecine
// konuştuğu HTTP istemcisidir. Web katmanı veritabanına DEĞİL bu istemciye
// bağlanır: Client, web.IdeaStore arayüzünü uygular.
//
// Hata sözleşmesi: kayıt yoksa store.ErrNotFound (web 404 render eder),
// diğer her şey (ağ hatası, 5xx, bozuk JSON) sarılı hata olarak döner ve
// web katmanında 502 sayfasına çevrilir.
package apiclient

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
	"github.com/musaay/idealode/api/internal/web"
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
// `parent_idea_id` ve `mine` alanları store.Idea üzerinde çözülür.
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

// msgDTO, sohbet mesajının tel üzerindeki biçimi (sözleşmedeki `Msg`).
type msgDTO struct {
	ID        int64     `json:"id"` // api'de store.ChatMessage.ID (BIGSERIAL) — sayı, string değil
	Role      string    `json:"role"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func (m msgDTO) toWeb() web.ChatMessage {
	return web.ChatMessage{ID: strconv.FormatInt(m.ID, 10), Role: m.Role, Message: m.Message, CreatedAt: m.CreatedAt}
}

// ListChat, `GET /api/ideas/{id}/chat` — bu oturumun kart sohbeti.
func (c *Client) ListChat(ctx context.Context, ideaID int64) ([]web.ChatMessage, error) {
	var body struct {
		Messages []msgDTO `json:"messages"`
	}
	path := "/api/ideas/" + strconv.FormatInt(ideaID, 10) + "/chat"
	if err := c.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	out := make([]web.ChatMessage, 0, len(body.Messages))
	for _, m := range body.Messages {
		out = append(out, m.toWeb())
	}
	return out, nil
}

// SendChat, `POST /api/ideas/{id}/chat` — mesaj gönderir, cevabı ve en
// fazla 3 öneriyi döner.
func (c *Client) SendChat(ctx context.Context, ideaID int64, message, lang string) (web.ChatReply, error) {
	var body struct {
		Reply       msgDTO   `json:"reply"`
		Suggestions []string `json:"suggestions"`
	}
	path := "/api/ideas/" + strconv.FormatInt(ideaID, 10) + "/chat"
	req := map[string]string{"message": message, "lang": lang}
	if err := c.post(ctx, path, req, &body); err != nil {
		return web.ChatReply{}, err
	}
	return web.ChatReply{Reply: body.Reply.toWeb(), Suggestions: body.Suggestions}, nil
}

// Blend, `POST /api/ideas/{id}/blend` — sohbetten yeni `ai_blended` kart.
func (c *Client) Blend(ctx context.Context, ideaID int64, lang string) (*store.Idea, error) {
	var body struct {
		Idea *store.Idea `json:"idea"`
	}
	path := "/api/ideas/" + strconv.FormatInt(ideaID, 10) + "/blend"
	if err := c.post(ctx, path, map[string]string{"lang": lang}, &body); err != nil {
		return nil, err
	}
	if body.Idea == nil || body.Idea.ID <= 0 {
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
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

// post, JSON gövdeli POST isteğini yapar ve yanıtı out'a çözer.
func (c *Client) post(ctx context.Context, path string, in any, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, in, out)
}

// do, tek isteği yapar. Oturum kimliği ctx'ten okunup `X-Session-Id`
// başlığına yazılır (api başlıksız isteği 400 ile reddeder).
//
// Durum eşlemesi: 404 -> store.ErrNotFound; 400 -> web.ErrBadRequest;
// 409 -> web.ErrNoConversation; 429 -> web.ErrRateLimited;
// 502 -> web.ErrUpstream; diğer her şey sarılı hata (web 502 sayfası).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, in any, out any) error {
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	var payload io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("api isteği hazırlanamadı (%s): %w", path, err)
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("api isteği hazırlanamadı (%s): %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sid := web.SessionFromContext(ctx); sid != "" {
		req.Header.Set("X-Session-Id", sid)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// ctx iptali/zaman aşımı buraya düşer; sarmalanarak korunur.
		return fmt.Errorf("api'ye ulaşılamadı (%s): %w", path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
	}()

	if err := statusError(path, resp.StatusCode); err != nil {
		return err
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

// statusError, HTTP durumunu sözleşmedeki tipli hataya çevirir; başarı
// durumlarında (200/201) nil döner.
func statusError(path string, code int) error {
	switch code {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("api (%s): %w", path, store.ErrNotFound)
	case http.StatusBadRequest:
		return fmt.Errorf("api (%s): %w", path, web.ErrBadRequest)
	case http.StatusConflict:
		return fmt.Errorf("api (%s): %w", path, web.ErrNoConversation)
	case http.StatusTooManyRequests:
		return fmt.Errorf("api (%s): %w", path, web.ErrRateLimited)
	case http.StatusBadGateway:
		return fmt.Errorf("api (%s): %w", path, web.ErrUpstream)
	default:
		return fmt.Errorf("api beklenmeyen durum (%s): %d", path, code)
	}
}
