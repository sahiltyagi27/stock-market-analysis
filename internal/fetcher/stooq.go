package fetcher

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// StooqFetcher downloads historical daily OHLCV data from stooq.com.
// No authentication is required. Supports NSE (.ns), BSE (.bo), and US symbols.
//
// Symbol mapping: "HDFCBANK.NS" is queried as "hdfcbank.ns" (Stooq uses lowercase),
// but stored as "HDFCBANK" (exchange suffix stripped via NormalizeSymbol).
//
// BaseURL and HTTPClient are exported for test injection.
type StooqFetcher struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewStooqFetcher returns a StooqFetcher with production defaults.
func NewStooqFetcher() *StooqFetcher {
	return &StooqFetcher{
		BaseURL:    "https://stooq.com",
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// FetchDaily downloads up to `period` of daily candles for the given symbol.
// Symbol is normalised (e.g. "HDFCBANK.NS" → "HDFCBANK") before being stored.
// period examples: "2y", "6m", "90d".
//
// Stooq returns rows in descending date order; this method sorts them ascending
// so callers always receive chronological data regardless of source behaviour.
func (f *StooqFetcher) FetchDaily(symbol, period string) ([]models.Candle, error) {
	now := time.Now()
	start, err := ParsePeriod(period, now)
	if err != nil {
		return nil, err
	}

	// Stooq requires lowercase symbols (e.g. "hdfcbank.ns").
	stooqSym := strings.ToLower(strings.TrimSpace(symbol))

	dlURL := fmt.Sprintf(
		"%s/q/d/l/?s=%s&d1=%s&d2=%s&i=d",
		f.BaseURL,
		stooqSym,
		start.Format("20060102"),
		now.Format("20060102"),
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

	candles, err := loader.Parse(resp.Body, NormalizeSymbol(symbol))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", symbol, err)
	}

	// Always sort ascending regardless of source row order.
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Timestamp.Before(candles[j].Timestamp)
	})

	return candles, nil
}
