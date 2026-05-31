// Package fetcher downloads historical daily OHLCV candles from external sources.
// The Fetcher interface is the primary abstraction; YahooFetcher is the default implementation.
package fetcher

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Fetcher downloads historical daily OHLCV data for a symbol.
type Fetcher interface {
	FetchDaily(symbol string, period string) ([]models.Candle, error)
}

// YahooFetcher downloads from the Yahoo Finance CSV endpoint.
//
// BaseURL and HTTPClient are exported so they can be overridden in tests.
// Crumb can be pre-set to skip the two-step auth handshake (useful in tests).
// When Crumb is empty, it is fetched automatically on the first FetchDaily call.
type YahooFetcher struct {
	BaseURL    string
	HTTPClient *http.Client
	// Crumb is the Yahoo Finance session crumb. Leave empty for auto-fetch;
	// set to any non-empty string in tests to bypass the real handshake.
	Crumb string

	crumbOnce sync.Once
	crumbErr  error
}

// NewYahooFetcher returns a YahooFetcher with production defaults.
// A cookie jar is attached so the session cookie acquired during the crumb
// handshake is automatically included in every subsequent download request.
func NewYahooFetcher() *YahooFetcher {
	jar, _ := cookiejar.New(nil)
	return &YahooFetcher{
		BaseURL: "https://query1.finance.yahoo.com",
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}
}

// ensureCrumb runs the Yahoo Finance auth handshake exactly once per fetcher
// instance. If Crumb is already set (e.g. in tests), the network call is skipped.
func (f *YahooFetcher) ensureCrumb() error {
	f.crumbOnce.Do(func() {
		if f.Crumb != "" {
			return // pre-set; skip real handshake
		}
		f.Crumb, f.crumbErr = f.fetchCrumb()
	})
	return f.crumbErr
}

// fetchCrumb performs the two-step Yahoo Finance handshake:
//  1. GET https://finance.yahoo.com/ to acquire session cookies.
//  2. GET {BaseURL}/v1/test/getcrumb — the cookie jar sends the cookies
//     automatically, and Yahoo returns a short crumb token in the body.
func (f *YahooFetcher) fetchCrumb() (string, error) {
	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

	// Step 1: visit Yahoo Finance to receive session cookies.
	req, err := http.NewRequest(http.MethodGet, "https://finance.yahoo.com/", nil)
	if err != nil {
		return "", fmt.Errorf("build consent request: %w", err)
	}
	req.Header.Set("User-Agent", ua)
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("init Yahoo session: %w", err)
	}
	resp.Body.Close()

	// Step 2: exchange session cookies for a crumb token.
	req2, err := http.NewRequest(http.MethodGet, f.BaseURL+"/v1/test/getcrumb", nil)
	if err != nil {
		return "", fmt.Errorf("build crumb request: %w", err)
	}
	req2.Header.Set("User-Agent", ua)
	resp2, err := f.HTTPClient.Do(req2)
	if err != nil {
		return "", fmt.Errorf("get crumb: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp2.Body, 256))
		return "", fmt.Errorf("get crumb: HTTP %d: %s", resp2.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", fmt.Errorf("read crumb: %w", err)
	}
	crumb := strings.TrimSpace(string(body))
	if crumb == "" {
		return "", fmt.Errorf("Yahoo returned an empty crumb")
	}
	return crumb, nil
}

// FetchDaily downloads up to `period` of daily candles for the given symbol.
// Symbol is normalised (e.g. "HDFCBANK.NS" → "HDFCBANK") before being stored.
// period examples: "2y", "6m", "90d".
func (f *YahooFetcher) FetchDaily(symbol, period string) ([]models.Candle, error) {
	now := time.Now()
	start, err := ParsePeriod(period, now)
	if err != nil {
		return nil, err
	}

	if err := f.ensureCrumb(); err != nil {
		return nil, fmt.Errorf("Yahoo auth: %w", err)
	}

	dlURL := fmt.Sprintf(
		"%s/v7/finance/download/%s?period1=%d&period2=%d&interval=1d&events=history&crumb=%s",
		f.BaseURL, symbol, start.Unix(), now.Unix(), url.QueryEscape(f.Crumb),
	)

	req, err := http.NewRequest(http.MethodGet, dlURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", symbol, err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("fetch %s: HTTP %d: %s", symbol, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return loader.Parse(resp.Body, NormalizeSymbol(symbol))
}

// NormalizeSymbol strips exchange suffixes (.NS, .BO, .L, etc.) and uppercases.
// Examples: "HDFCBANK.NS" → "HDFCBANK", "AAPL" → "AAPL".
func NormalizeSymbol(symbol string) string {
	if i := strings.IndexByte(symbol, '.'); i != -1 {
		symbol = symbol[:i]
	}
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// ParsePeriod parses a period string ("2y", "6m", "90d") into a start time
// relative to `from`. Supported units: y (years), m (months), d (days).
func ParsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: must be like 2y, 6m, 90d", period)
	}
	unit := period[len(period)-1]
	n, err := strconv.Atoi(period[:len(period)-1])
	if err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid period %q: number must be a positive integer", period)
	}
	switch unit {
	case 'y', 'Y':
		return from.AddDate(-n, 0, 0), nil
	case 'm', 'M':
		return from.AddDate(0, -n, 0), nil
	case 'd', 'D':
		return from.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period unit %q in %q: use y, m, or d", string(unit), period)
	}
}
