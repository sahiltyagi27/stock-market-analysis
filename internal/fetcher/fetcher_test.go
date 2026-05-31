package fetcher_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/fetcher"
)

// yahooCsv is a minimal Yahoo Finance CSV fixture (includes "Adj Close" to
// verify the loader handles the extra column correctly).
const yahooCsv = `Date,Open,High,Low,Close,Adj Close,Volume
2024-01-02,785.00,792.50,780.10,788.30,788.30,12345678
2024-01-03,788.50,795.00,783.20,791.60,791.60,9876543
2024-01-04,791.00,798.40,787.30,794.20,794.20,11234567
`

// ---------------------------------------------------------------------------
// NormalizeSymbol
// ---------------------------------------------------------------------------

func TestNormalizeSymbol_StripsNSSuffix(t *testing.T) {
	got := fetcher.NormalizeSymbol("HDFCBANK.NS")
	if got != "HDFCBANK" {
		t.Errorf("NormalizeSymbol(%q) = %q, want HDFCBANK", "HDFCBANK.NS", got)
	}
}

func TestNormalizeSymbol_StripsMultiSuffix(t *testing.T) {
	cases := map[string]string{
		"RELIANCE.NS":   "RELIANCE",
		"AAPL":          "AAPL",
		"BP.L":          "BP",
		"BAJAJ-AUTO.NS": "BAJAJ-AUTO",
	}
	for input, want := range cases {
		got := fetcher.NormalizeSymbol(input)
		if got != want {
			t.Errorf("NormalizeSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeSymbol_Uppercases(t *testing.T) {
	got := fetcher.NormalizeSymbol("hdfcbank.ns")
	if got != "HDFCBANK" {
		t.Errorf("NormalizeSymbol(%q) = %q, want HDFCBANK", "hdfcbank.ns", got)
	}
}

// ---------------------------------------------------------------------------
// ParsePeriod
// ---------------------------------------------------------------------------

func TestParsePeriod_Years(t *testing.T) {
	ref := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	got, err := fetcher.ParsePeriod("2y", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2022, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ParsePeriod(2y) = %v, want %v", got, want)
	}
}

func TestParsePeriod_Months(t *testing.T) {
	ref := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	got, err := fetcher.ParsePeriod("6m", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2023, 12, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ParsePeriod(6m) = %v, want %v", got, want)
	}
}

func TestParsePeriod_Days(t *testing.T) {
	ref := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	got, err := fetcher.ParsePeriod("90d", ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := ref.AddDate(0, 0, -90)
	if !got.Equal(want) {
		t.Errorf("ParsePeriod(90d) = %v, want %v", got, want)
	}
}

func TestParsePeriod_InvalidFormat(t *testing.T) {
	cases := []string{"", "y", "abc", "2x", "-1y", "0m"}
	for _, p := range cases {
		_, err := fetcher.ParsePeriod(p, time.Now())
		if err == nil {
			t.Errorf("ParsePeriod(%q): expected error, got nil", p)
		}
	}
}

// ---------------------------------------------------------------------------
// YahooFetcher — FetchDaily with httptest server (no real network calls)
// ---------------------------------------------------------------------------

func newTestServer(body string, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, body)
	}))
}

func TestFetchDaily_ParsesCandlesCorrectly(t *testing.T) {
	srv := newTestServer(yahooCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.YahooFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient, Crumb: "testcrumb"}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candles) != 3 {
		t.Errorf("got %d candles, want 3", len(candles))
	}
}

func TestFetchDaily_NormalizesSymbol(t *testing.T) {
	srv := newTestServer(yahooCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.YahooFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient, Crumb: "testcrumb"}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candles {
		if c.Symbol != "HDFCBANK" {
			t.Errorf("candle symbol = %q, want HDFCBANK (suffix must be stripped)", c.Symbol)
		}
	}
}

func TestFetchDaily_ParsesCandleFields(t *testing.T) {
	srv := newTestServer(yahooCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.YahooFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient, Crumb: "testcrumb"}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := candles[0]
	if c.Open != 785.00 {
		t.Errorf("Open = %.2f, want 785.00", c.Open)
	}
	if c.High != 792.50 {
		t.Errorf("High = %.2f, want 792.50", c.High)
	}
	if c.Low != 780.10 {
		t.Errorf("Low = %.2f, want 780.10", c.Low)
	}
	if c.Close != 788.30 {
		t.Errorf("Close = %.2f, want 788.30", c.Close)
	}
	if c.Volume != 12345678 {
		t.Errorf("Volume = %d, want 12345678", c.Volume)
	}
}

func TestFetchDaily_HTTPErrorReturnsError(t *testing.T) {
	srv := newTestServer(`{"error":"Not Found"}`, http.StatusNotFound)
	defer srv.Close()

	f := &fetcher.YahooFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient, Crumb: "testcrumb"}
	_, err := f.FetchDaily("UNKNOWN.NS", "2y")
	if err == nil {
		t.Error("expected error for HTTP 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestFetchDaily_InvalidPeriodReturnsError(t *testing.T) {
	f := fetcher.NewYahooFetcher()
	_, err := f.FetchDaily("HDFCBANK.NS", "bad")
	if err == nil {
		t.Error("expected error for invalid period, got nil")
	}
}

// ---------------------------------------------------------------------------
// StooqFetcher — FetchDaily with httptest server (no real network calls)
// ---------------------------------------------------------------------------

// stooqCsv is a minimal Stooq CSV fixture with rows in descending date order
// (newest first), which is what stooq.com returns. StooqFetcher must sort
// them ascending before returning.
const stooqCsv = `Date,Open,High,Low,Close,Volume
2024-01-04,791.00,798.40,787.30,794.20,11234567
2024-01-03,788.50,795.00,783.20,791.60,9876543
2024-01-02,785.00,792.50,780.10,788.30,12345678
`

func newStooqTestServer(body string, statusCode int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, body)
	}))
}

func TestStooqFetchDaily_ParsesCandlesCorrectly(t *testing.T) {
	srv := newStooqTestServer(stooqCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candles) != 3 {
		t.Errorf("got %d candles, want 3", len(candles))
	}
}

func TestStooqFetchDaily_SortsAscending(t *testing.T) {
	srv := newStooqTestServer(stooqCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// stooqCsv is newest-first; after sort we expect oldest first.
	for i := 1; i < len(candles); i++ {
		if !candles[i].Timestamp.After(candles[i-1].Timestamp) {
			t.Errorf("candles[%d] (%v) is not after candles[%d] (%v): data must be ascending",
				i, candles[i].Timestamp, i-1, candles[i-1].Timestamp)
		}
	}
}

func TestStooqFetchDaily_NormalizesSymbol(t *testing.T) {
	srv := newStooqTestServer(stooqCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candles {
		if c.Symbol != "HDFCBANK" {
			t.Errorf("candle symbol = %q, want HDFCBANK (suffix must be stripped)", c.Symbol)
		}
	}
}

func TestStooqFetchDaily_ParsesCandleFields(t *testing.T) {
	srv := newStooqTestServer(stooqCsv, http.StatusOK)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	candles, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After ascending sort, index 0 = 2024-01-02 (oldest row in fixture).
	c := candles[0]
	if c.Open != 785.00 {
		t.Errorf("Open = %.2f, want 785.00", c.Open)
	}
	if c.High != 792.50 {
		t.Errorf("High = %.2f, want 792.50", c.High)
	}
	if c.Volume != 12345678 {
		t.Errorf("Volume = %d, want 12345678", c.Volume)
	}
}

func TestStooqFetchDaily_HTTPErrorReturnsError(t *testing.T) {
	srv := newStooqTestServer(`not found`, http.StatusNotFound)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	_, err := f.FetchDaily("UNKNOWN.NS", "2y")
	if err == nil {
		t.Error("expected error for HTTP 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestStooqFetchDaily_APIKeyRequiredReturnsHelpfulError(t *testing.T) {
	body := `Get your apikey:

1. Open https://stooq.com/q/d/?s=hdfcbank.ns&get_apikey
2. Enter the captcha code.
3. Copy the CSV download link at the bottom of the page - it will contain the <apikey> variable.
`
	srv := newStooqTestServer(body, http.StatusOK)
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient}
	_, err := f.FetchDaily("HDFCBANK.NS", "2y")
	if !errors.Is(err, fetcher.ErrStooqAPIKeyRequired) {
		t.Fatalf("expected ErrStooqAPIKeyRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "STOOQ_API_KEY") {
		t.Errorf("error should mention STOOQ_API_KEY, got: %v", err)
	}
}

func TestNewStooqFetcher_ReadsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("STOOQ_API_KEY", "test-key")

	f := fetcher.NewStooqFetcher()
	if f.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want test-key", f.APIKey)
	}
}

func TestStooqFetchDaily_SendsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("apikey"); got != "test-key" {
			t.Errorf("apikey query = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprint(w, stooqCsv)
	}))
	defer srv.Close()

	f := &fetcher.StooqFetcher{BaseURL: srv.URL, HTTPClient: http.DefaultClient, APIKey: "test-key"}
	if _, err := f.FetchDaily("HDFCBANK.NS", "2y"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStooqFetchDaily_InvalidPeriodReturnsError(t *testing.T) {
	f := fetcher.NewStooqFetcher()
	_, err := f.FetchDaily("HDFCBANK.NS", "bad")
	if err == nil {
		t.Error("expected error for invalid period, got nil")
	}
}
