// Package fetcher downloads historical daily OHLCV candles from external sources.
// The Fetcher interface is the primary abstraction; YahooFetcher is the default implementation.
package fetcher

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Fetcher downloads historical daily OHLCV data for a symbol.
type Fetcher interface {
	FetchDaily(symbol string, period string) ([]models.Candle, error)
}

// YahooFetcher downloads from the Yahoo Finance CSV endpoint.
// BaseURL and HTTPClient are exported so they can be overridden in tests.
type YahooFetcher struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewYahooFetcher returns a YahooFetcher with production defaults.
func NewYahooFetcher() *YahooFetcher {
	return &YahooFetcher{
		BaseURL:    "https://query1.finance.yahoo.com",
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
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

	url := fmt.Sprintf(
		"%s/v7/finance/download/%s?period1=%d&period2=%d&interval=1d&events=history",
		f.BaseURL, symbol, start.Unix(), now.Unix(),
	)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", symbol, err)
	}
	// Yahoo Finance blocks requests without a user-agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; stock-analysis-bot/0.1)")

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
