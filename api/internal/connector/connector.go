// Package connector, platform-agnostik ingestion katmanı. Her platform
// SourceConnector'ı uygular; platform eklendikçe raw_posts şeması ve üst
// katmanlar DEĞİŞMEZ (plan madde 2).
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/musaay/idealode/api/internal/store"
)

// initialWindow: bir kaynağın last_seen_ref'i yokken (ilk çekim) geriye
// dönük tarama penceresi.
const initialWindow = 7 * 24 * time.Hour

const userAgent = "idealode/0.1 (+https://github.com/musaay/idealode)"

// SourceConnector, tek bir kaynaktan (sources satırı) yeni içerik çeker.
// Dönen ikinci değer kaynağın yeni last_seen_ref'idir; boşsa ilerleme yok
// demektir ve mevcut ref korunur.
type SourceConnector interface {
	Platform() string
	FetchNew(ctx context.Context, src store.Source) ([]store.RawPost, string, error)
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// getJSON, GET isteği atar, 2xx dışını hataya çevirir ve gövdeyi target'a
// decode eder.
func getJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	return doJSON(client, req, target)
}

// doJSON, hazırlanmış isteği çalıştırır, 2xx dışını hataya çevirir ve
// gövdeyi target'a decode eder.
func doJSON(client *http.Client, req *http.Request, target any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, target)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
