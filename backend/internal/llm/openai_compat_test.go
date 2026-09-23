package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatJSONRetryOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	out, err := c.ChatJSON(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("içerik yanlış: %q", out)
	}
	if calls.Load() != 2 {
		t.Errorf("429 sonrası retry beklenirdi; çağrı sayısı: %d", calls.Load())
	}
}

func TestChatJSONNonRetryableError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "bad", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSON(context.Background(), "s", "u"); err == nil {
		t.Fatal("401'de hata beklenir")
	}
	if calls.Load() != 1 {
		t.Errorf("401 retry edilmemeli; çağrı sayısı: %d", calls.Load())
	}
}

func TestChatJSONWithTemperatureSendsZero(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSONWithTemperature(context.Background(), "sys", "user", 0); err != nil {
		t.Fatalf("ChatJSONWithTemperature: %v", err)
	}
	// omitempty KULLANILMAZ: temperature 0 iken de gövdede görünmeli (#106).
	if !strings.Contains(string(body), `"temperature":0`) {
		t.Errorf("gövdede \"temperature\":0 bekleniyordu, geldi: %s", body)
	}
}

func TestChatJSONUsesDefaultTemperature(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, err := c.ChatJSON(context.Background(), "sys", "user"); err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if !strings.Contains(string(body), `"temperature":0.3`) {
		t.Errorf("gövdede \"temperature\":0.3 bekleniyordu, geldi: %s", body)
	}
}

// groqRequestTooLargeBody, #156 issue'sunda karşılaşılan GERÇEK Groq 413
// gövdesidir (birebir) — "type":"tokens","code":"rate_limit_exceeded" alanları
// yanıltıcı biçimde kota hatasına benziyor olsa da bu bir TEK İSTEK boyut
// hatasıdır (429 DEĞİL, HTTP 413), IsRequestTooLarge bunu ayırt etmeli.
const groqRequestTooLargeBody = "{\"error\":{\"message\":\"Request too large for model `openai/gpt-oss-120b` in organization `org_test` service tier `on_demand` on tokens per minute (TPM): Limit 8000, Requested 8112, please reduce your message size and try again.\",\"type\":\"tokens\",\"code\":\"rate_limit_exceeded\"}}"

// TestChatJSON413NotRetriedButFlagged, 413 (istek çok büyük) alındığında
// AYNI istek içeride tekrar denenmediğini (retryable=false — tekrar denemek
// anlamsız, aynı boyut yine aşar) ama hatanın llm.IsRequestTooLarge ile
// ayırt edilebildiğini doğrular (#156) — pipeline paketi bunu parti bölme
// tetikleyicisi olarak kullanır. Gövde, issue'daki GERÇEK Groq yanıtının
// birebir aynısıdır: "code":"rate_limit_exceeded" alanı yanıltıcı olsa da
// (429 rateLimitError İLE KARIŞTIRILMAMALI — HTTP durumu 413'tür) bu bir
// istek boyutu hatasıdır.
func TestChatJSON413NotRetriedButFlagged(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte(groqRequestTooLargeBody))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.ChatJSON(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("413'te hata beklenir")
	}
	if !IsRequestTooLarge(err) {
		t.Errorf("IsRequestTooLarge true dönmeliydi, err: %v", err)
	}
	if _, isRateLimit := err.(*rateLimitError); isRateLimit {
		t.Errorf("gövdedeki \"code\":\"rate_limit_exceeded\" yanıltmamalı — 413 rateLimitError'a dönüşmemeli, err: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("413 içeride tekrar denenmemeli (parti bölme pipeline'da olur); çağrı sayısı: %d", calls.Load())
	}
}

// TestChatJSON413DetectedFrom400Body, bazı OpenAI-uyumlu sağlayıcıların aynı
// hatayı gerçek 413 yerine 400 + "Request too large" gövdesiyle
// dönebileceği durumu kapsar (#156 savunmacı ayırt etme).
func TestChatJSON413DetectedFrom400Body(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"Request too large for model, TPM limit exceeded"}}`))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.ChatJSON(context.Background(), "sys", "user")
	if !IsRequestTooLarge(err) {
		t.Errorf("400 + \"Request too large\" gövdesi de IsRequestTooLarge=true vermeliydi, err: %v", err)
	}
}

// TestChatJSON429IsNotRequestTooLarge, 429 (dakikalık/günlük kota) hatasının
// IsRequestTooLarge tarafından YAKALANMADIĞINI doğrular — pipeline bu ikisini
// ayırmak zorunda (#156 kabul kriteri: 429'da bölme YAPILMASIN). doRequest
// doğrudan çağrılır (ChatJSON'un içteki retry/backoff döngüsünü — ayrı
// TestChatJSONRetryOn429'da zaten kapsanıyor — atlayıp testi hızlı tutmak
// için).
func TestChatJSON429IsNotRequestTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, _, retryable, err := c.doRequest(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("429'da hata beklenir")
	}
	if !retryable {
		t.Error("429 retryable=true olmalı (mevcut backoff davranışı)")
	}
	if IsRequestTooLarge(err) {
		t.Errorf("429 asla IsRequestTooLarge=true vermemeli, err: %v", err)
	}
}

// groqJSONValidateFailedBody, #158 issue'sunda karşılaşılan GERÇEK Groq 400
// json_validate_failed gövdesidir (birebir) — model geçerli JSON
// üretemediğinde döner, failed_generation genelde boştur.
const groqJSONValidateFailedBody = `{"error":{"message":"Failed to validate JSON. Please adjust your prompt. See 'failed_generation' for more details.","type":"invalid_request_error","code":"json_validate_failed","failed_generation":""}}`

// TestChatJSON400JSONValidateFailedNotRetriedButFlagged, 400
// json_validate_failed alındığında AYNI istek içeride tekrar denenmediğini
// (retryable=false — pipeline katmanı partiyi bölerek yeniden dener) ama
// hatanın llm.IsJSONValidateFailed ile ayırt edilebildiğini doğrular (#158).
// Gövde, issue'daki GERÇEK Groq yanıtının birebir aynısıdır.
func TestChatJSON400JSONValidateFailedNotRetriedButFlagged(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(groqJSONValidateFailedBody))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.ChatJSON(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("400 json_validate_failed'de hata beklenir")
	}
	if !IsJSONValidateFailed(err) {
		t.Errorf("IsJSONValidateFailed true dönmeliydi, err: %v", err)
	}
	if IsRequestTooLarge(err) {
		t.Errorf("json_validate_failed IsRequestTooLarge=true vermemeli (ayrı hata türü), err: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("400 json_validate_failed içeride tekrar denenmemeli (parti bölme pipeline'da olur); çağrı sayısı: %d", calls.Load())
	}
}

// TestChatJSON400OtherCodeIsNotJSONValidateFailed, json_validate_failed
// DIŞINDAKİ bir 400'ün (ör. farklı bir "code" alanı ya da hiç "code" alanı
// olmayan bir hata) IsJSONValidateFailed tarafından YAKALANMADIĞINI
// doğrular — pipeline yalnız bu belirli hatada parti bölmeli (#158 kabul
// kriteri: diğer 400'lerde bölme yok).
func TestChatJSON400OtherCodeIsNotJSONValidateFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"model does not exist","type":"invalid_request_error","code":"model_not_found"}}`))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, err := c.ChatJSON(context.Background(), "sys", "user")
	if err == nil {
		t.Fatal("400'de hata beklenir")
	}
	if IsJSONValidateFailed(err) {
		t.Errorf("json_validate_failed DIŞINDAKİ 400 IsJSONValidateFailed=true vermemeli, err: %v", err)
	}
}

// TestIsRateLimited429, doRequest doğrudan çağrılır (ChatJSON'un içteki
// retry/backoff döngüsünü — ayrı TestChatJSONRetryOn429'da zaten kapsanıyor
// — atlayıp testi hızlı tutmak için, bkz. TestChatJSON429IsNotRequestTooLarge
// deseni). 429'un IsRateLimited=true verdiğini doğrular (#175) — lens-ab'nin
// kendi bekle-dene döngüsünü tetikleyen imza budur.
func TestIsRateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, _, _, err := c.doRequest(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("429'da hata beklenir")
	}
	if !IsRateLimited(err) {
		t.Errorf("IsRateLimited true dönmeliydi, err: %v", err)
	}
}

// TestIsRateLimitedGeminiResourceExhausted, Gemini'nin oran sınırını farklı
// bir gövde imzasıyla ("RESOURCE_EXHAUSTED") dönebildiği durumda da
// IsRateLimited'ın true verdiğini doğrular (#175).
func TestIsRateLimitedGeminiResourceExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, _, _, err := c.doRequest(context.Background(), []byte(`{}`))
	if !IsRateLimited(err) {
		t.Errorf("RESOURCE_EXHAUSTED gövdesi IsRateLimited=true vermeliydi, err: %v", err)
	}
}

// TestIsRateLimitedNotFor5xx, düz 5xx (RESOURCE_EXHAUSTED imzası olmayan)
// hatanın IsRateLimited tarafından YAKALANMADIĞINI doğrular — lens-ab'de
// "oran sınırı dışı hata" yoluna düşmesi gerekiyor (bilinçli ayrım, #175).
func TestIsRateLimitedNotFor5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer srv.Close()

	c := &OpenAICompatClient{APIKey: "test", Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()}
	_, _, _, err := c.doRequest(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("500'de hata beklenir")
	}
	if IsRateLimited(err) {
		t.Errorf("düz 5xx IsRateLimited=true vermemeliydi, err: %v", err)
	}
}

func TestHostFallsBackToBaseURL(t *testing.T) {
	// Geçersiz/host'suz bir BaseURL verilirse hata metni yine anlamlı kalsın.
	c := &OpenAICompatClient{BaseURL: "not-a-url"}
	if got := c.host(); got != "not-a-url" {
		t.Errorf("host() ham BaseURL'e düşmeli, geldi: %q", got)
	}

	c2 := &OpenAICompatClient{BaseURL: "https://api.example.com/v1"}
	if got := c2.host(); got != "api.example.com" {
		t.Errorf("host() parse edilmiş host dönmeli, geldi: %q", got)
	}
}
