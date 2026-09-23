// Package llm, OpenAI'ın chat-completions şemasını konuşan istemci sağlar.
// Sağlayıcı (base URL/model/API key) config üzerinden env'den gelir (#96) —
// sağlayıcı değişimi kod değişikliği istemez. "Sakin ilerleme" prensibi
// (plan madde 5, cv-search pattern reuse): exponential backoff + retry-on-429,
// Retry-After header'ına uyum; sleep + retry BİRLİKTE (reprocess_cvs hatası
// tekrarlanmaz).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Chat, pipeline'ın LLM bağımlılığını soyutlar (testlerde sahte uygulama).
//
// Sıcaklık politikası (#106): yargı çağrıları (sınıflandırma, tutarlılık,
// dedup, mercekler, fuse hakemleri) ChatJSONWithTemperature ile 0 gönderir
// (aynı girdiye tutarlı karar); üretim çağrıları (kart/sohbet metni)
// ChatJSON ile 0.3'te kalır (metin çeşitliliği).
type Chat interface {
	// ChatJSON, JSON-mode'da tek tur sohbet yapar ve modelin ürettiği ham
	// JSON string'ini döner (sabit 0.3 sıcaklıkla — üretim çağrıları için).
	ChatJSON(ctx context.Context, system, user string) (string, error)

	// ChatJSONWithTemperature, ChatJSON ile aynıdır ama sıcaklığı çağıran
	// belirler (yargı çağrıları için 0).
	ChatJSONWithTemperature(ctx context.Context, system, user string, temperature float64) (string, error)
}

// OpenAICompatClient, OpenAI'ın chat-completions şemasıyla uyumlu herhangi
// bir endpoint'i kullanır (base URL config'ten gelir, test için override
// edilebilir).
type OpenAICompatClient struct {
	APIKey     string
	Model      string
	BaseURL    string // test için override edilebilir
	HTTPClient *http.Client
}

// NamedChat, bir Chat uygulamasının model adını okumak için opsiyonel
// arayüz (#166) — hangi modelin karar verdiğini kalıcı kayda
// (store.LensVerdict.Model) yazmak isteyen çağıran, chat.(NamedChat) type
// assertion'ıyla dener; OpenAICompatClient bunu uygular, sahte test
// istemcileri de isterse uygulayabilir. Uygulamayan istemcilerde (ör. eski
// sahte test chat'leri) alan boş kalır — davranış ETKİLENMEZ.
type NamedChat interface {
	ModelName() string
}

// ModelName, OpenAICompatClient'ı NamedChat yapar.
func (c *OpenAICompatClient) ModelName() string { return c.Model }

// NewOpenAICompat canlı istemci döner.
func NewOpenAICompat(baseURL, apiKey, model string) *OpenAICompatClient {
	return &OpenAICompatClient{
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
	}
}

const maxRetries = 3

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type respFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// defaultTemperature, üretim çağrıları (kart/sohbet metni) için sabit
// sıcaklıktır — metin çeşitliliği istenir (#106).
const defaultTemperature = 0.3

func (c *OpenAICompatClient) ChatJSON(ctx context.Context, system, user string) (string, error) {
	return c.ChatJSONWithTemperature(ctx, system, user, defaultTemperature)
}

func (c *OpenAICompatClient) ChatJSONWithTemperature(ctx context.Context, system, user string, temperature float64) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature:    temperature,
		ResponseFormat: &respFormat{Type: "json_object"},
	})
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelay(lastErr, attempt)):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		content, usage, retryable, err := c.doRequest(ctx, payload)
		if err == nil {
			// Aynı mantıksal çağrı birkaç denemede başarılı olsa da yalnız
			// BAŞARILI cevabın usage'ı sayılır (#144) — retry'lerin harcadığı
			// token'lar bu çağrının usage'ında zaten yer almaz (sağlayıcı
			// başarısız denemeler için usage döndürmez).
			recordUsage(ctx, usage)
			return content, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("%s: %d denemede başarısız: %w", c.host(), maxRetries+1, lastErr)
}

// rateLimitError, Retry-After bilgisini backoff hesabına taşır.
type rateLimitError struct {
	host       string // hata metninde sağlayıcı adı yerine base URL host'u kullanılır
	status     int
	retryAfter time.Duration
	body       string
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("%s HTTP %d: %s", e.host, e.status, e.body)
}

func retryDelay(err error, attempt int) time.Duration {
	if rle, ok := err.(*rateLimitError); ok && rle.retryAfter > 0 {
		return rle.retryAfter
	}
	return time.Duration(1<<attempt) * time.Second // 2s, 4s, 8s
}

// IsRateLimited, err zincirinde (ChatJSON*'nin döndüğü, %w ile sarmalanmış
// hata dahil) oran sınırı (429) hatası olup olmadığını bildirir (#175) —
// `idealode lens-ab` bunu, istemcinin KENDİ retry/backoff'u (doRequest'teki
// maxRetries denemesi) TÜKENDİKTEN SONRA hâlâ oran sınırına takılan
// çağrıları AYRICA bekleyip aynı çağrıyı yeniden denemek için kullanır.
// rateLimitError hem 429'u hem 5xx'i taşır (bkz. doRequest) ama burada
// BİLEREK yalnız 429 (+ Gemini'nin oran sınırını 429 dışında bir HTTP
// statüsüyle de dönebildiği "RESOURCE_EXHAUSTED" gövde imzası) oran sınırı
// sayılır — düz 5xx (sağlayıcı iç hatası, farklı bir arıza sınıfı) lens-ab'de
// "oran sınırı dışı hata" yoluna düşer (bilinçli karar, gerekçe: lens-ab
// spec'i #175 — 5xx'i de aynı bekle-dene yoluna sokmak sağlayıcı çöküşünü
// oran sınırıyla karıştırıp gereksiz uzun beklemelere yol açabilir).
func IsRateLimited(err error) bool {
	var e *rateLimitError
	if !errors.As(err, &e) {
		return false
	}
	return e.status == http.StatusTooManyRequests || strings.Contains(e.body, "RESOURCE_EXHAUSTED")
}

// requestTooLargeError, sağlayıcının istek boyutu/dakikalık token (TPM)
// sınırını TEK istekte aştığını bildiren hatayı taşır (Groq: HTTP 413 ya da
// gövdede "Request too large" mesajı) (#156). rateLimitError'dan (429/5xx —
// dakikalık/günlük KOTA) BİLEREK ayrı tutulur: kota hatası partiyi bölmekle
// çözülmez (mevcut backoff/retry davranışı korunur), bu hata İSE partiyi
// bölüp yeniden denemeyi anlamlı kılar (bkz. pipeline.clusterBatch).
type requestTooLargeError struct {
	host string
	body string
}

func (e *requestTooLargeError) Error() string {
	return fmt.Sprintf("%s: istek çok büyük (413): %s", e.host, e.body)
}

// IsRequestTooLarge, err'nin sağlayıcıdan "istek çok büyük" (413 tarzı)
// hatası olup olmadığını bildirir — çağıran paket (pipeline) bunu parti
// bölme + yeniden deneme tetikleyicisi olarak kullanır (#156).
func IsRequestTooLarge(err error) bool {
	var e *requestTooLargeError
	return errors.As(err, &e)
}

// NewRequestTooLargeError, requestTooLargeError'ı DIŞ paketlere (pipeline
// testlerindeki sahte Chat uygulamaları) açar — gerçek 413 yanıtını
// simüle etmenin tek yolu budur, tür kendisi bilerek dışa kapalı kalır
// (#156).
func NewRequestTooLargeError(host, body string) error {
	return &requestTooLargeError{host: host, body: body}
}

// isRequestTooLargeStatus, HTTP durum kodu/gövdesinin "istek çok büyük"
// hatası olup olmadığını belirler. Öncelik gerçek 413 durum koduna; bazı
// OpenAI-uyumlu sağlayıcılar aynı hatayı 400 + "Request too large" gövdesiyle
// dönebildiğinden (#156), bu ikinci biçim de savunmacı olarak yakalanır.
func isRequestTooLargeStatus(status int, body string) bool {
	if status == http.StatusRequestEntityTooLarge {
		return true
	}
	return status >= 400 && status < 500 && strings.Contains(strings.ToLower(body), "request too large")
}

// jsonValidateFailedError, sağlayıcının (Groq) modelin geçerli JSON
// üretemediğini bildiren HTTP 400 + gövdede "code":"json_validate_failed"
// hatasını taşır (#158). requestTooLargeError'la AYNI mantıkla ele alınır:
// AYNI isteği burada tekrar denemek anlamsız (model yine aynı büyük/karmaşık
// girdide bocalar) — retryable=false, çağıran paket (pipeline) partiyi
// bölerek daha küçük istekler kurar. Diğer 400'ler (ör. kimlik doğrulama,
// geçersiz parametre) ETKİLENMEZ — yalnız bu belirli "code" ayırt edilir.
type jsonValidateFailedError struct {
	host string
	body string
}

func (e *jsonValidateFailedError) Error() string {
	return fmt.Sprintf("%s: LLM geçerli JSON üretemedi (400 json_validate_failed): %s", e.host, e.body)
}

// IsJSONValidateFailed, err'nin sağlayıcıdan "modelin ürettiği çıktı geçerli
// JSON değil" (400 json_validate_failed) hatası olup olmadığını bildirir —
// çağıran paket (pipeline) bunu, IsRequestTooLarge ile aynı şekilde, parti
// bölme + yeniden deneme tetikleyicisi olarak kullanır (#158).
func IsJSONValidateFailed(err error) bool {
	var e *jsonValidateFailedError
	return errors.As(err, &e)
}

// NewJSONValidateFailedError, jsonValidateFailedError'ı DIŞ paketlere
// (pipeline testlerindeki sahte Chat uygulamaları) açar — gerçek
// json_validate_failed yanıtını simüle etmenin tek yolu budur, tür kendisi
// bilerek dışa kapalı kalır (#158).
func NewJSONValidateFailedError(host, body string) error {
	return &jsonValidateFailedError{host: host, body: body}
}

// isJSONValidateFailedBody, HTTP 400 gövdesinin Groq'un
// "code":"json_validate_failed" hatası olup olmadığını belirler. Gövde ham
// JSON olarak ayrıştırılır (savunmacı — parse başarısızsa false döner,
// diğer 400 yolları etkilenmez); yalnız bu belirli "code" alanı eşleşirse
// true döner (#158).
func isJSONValidateFailedBody(body string) bool {
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return false
	}
	return parsed.Error.Code == "json_validate_failed"
}

func (c *OpenAICompatClient) doRequest(ctx context.Context, payload []byte) (content string, usage Usage, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", Usage{}, true, err // ağ hatası — denemeye değer
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", Usage{}, true, err
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		var after time.Duration
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, perr := strconv.ParseFloat(s, 64); perr == nil {
				after = time.Duration(secs * float64(time.Second))
			}
		}
		return "", Usage{}, true, &rateLimitError{host: c.host(), status: resp.StatusCode, retryAfter: after, body: truncate(string(body), 200)}
	}
	if isRequestTooLargeStatus(resp.StatusCode, string(body)) {
		// Aynı boyuttaki isteği burada TEKRAR denemek anlamsız (yine
		// aşacak) — retryable=false: pipeline katmanı IsRequestTooLarge ile
		// yakalayıp partiyi bölerek YENİ (daha küçük) istekler kuracak.
		return "", Usage{}, false, &requestTooLargeError{host: c.host(), body: truncate(string(body), 200)}
	}
	if resp.StatusCode == http.StatusBadRequest && isJSONValidateFailedBody(string(body)) {
		// Modelin ürettiği çıktı geçerli JSON değil — aynı isteği burada
		// TEKRAR denemek anlamsız (retryable=false): pipeline katmanı
		// IsJSONValidateFailed ile yakalayıp partiyi bölerek YENİ (daha
		// küçük/daha az karmaşık) istekler kuracak (#158).
		return "", Usage{}, false, &jsonValidateFailedError{host: c.host(), body: truncate(string(body), 200)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, false, fmt.Errorf("%s HTTP %d: %s", c.host(), resp.StatusCode, truncate(string(body), 400))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", Usage{}, false, fmt.Errorf("%s yanıtı parse edilemedi: %w", c.host(), err)
	}
	if parsed.Error != nil {
		return "", Usage{}, false, fmt.Errorf("%s: %s", c.host(), parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", Usage{}, false, fmt.Errorf("%s: boş yanıt", c.host())
	}
	return parsed.Choices[0].Message.Content, parsed.Usage, false, nil
}

// host, hata metinlerinde sağlayıcı adı yerine kullanılan base URL host'unu
// döner (#96 — sağlayıcı artık koda çakılı değil).
func (c *OpenAICompatClient) host() string {
	if u, err := url.Parse(c.BaseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.BaseURL
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
