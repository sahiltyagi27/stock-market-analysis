// Package kite integrates with Zerodha Kite Connect market data APIs.
package kite

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

const (
	defaultBaseURL = "https://api.kite.trade"
	kiteVersion    = "3"

	// Nifty50InstrumentToken is Kite's NSE index token for NIFTY 50.
	Nifty50InstrumentToken = int64(256265)
	// Nifty50Symbol is the normalized DB symbol used for NIFTY 50 candles.
	Nifty50Symbol = "NIFTY50"
)

// Client calls Kite Connect APIs.
type Client struct {
	BaseURL     string
	APIKey      string
	AccessToken string
	HTTPClient  *http.Client
}

// NewClient returns a Kite client with production defaults.
func NewClient(baseURL, apiKey, accessToken string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		AccessToken: accessToken,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Instrument is one row from Kite's instrument master CSV.
type Instrument struct {
	InstrumentToken int64
	TradingSymbol   string
	Name            string
	InstrumentType  string
	Segment         string
	Exchange        string
}

// Session is the subset of Kite's token exchange response used by this app.
type Session struct {
	UserID      string
	UserName    string
	AccessToken string
}

// ExchangeRequestToken exchanges a short-lived request_token for an access_token.
func (c *Client) ExchangeRequestToken(ctx context.Context, apiSecret, requestToken string) (Session, error) {
	if c.APIKey == "" || apiSecret == "" || requestToken == "" {
		return Session{}, fmt.Errorf("KITE_API_KEY, KITE_API_SECRET, and request_token are required")
	}

	checksum := sha256.Sum256([]byte(c.APIKey + requestToken + apiSecret))
	values := url.Values{}
	values.Set("api_key", c.APIKey)
	values.Set("request_token", requestToken)
	values.Set("checksum", fmt.Sprintf("%x", checksum))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/session/token", strings.NewReader(values.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("X-Kite-Version", kiteVersion)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("kite token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Session{}, fmt.Errorf("kite token exchange: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Status string `json:"status"`
		Data   struct {
			UserID      string `json:"user_id"`
			UserName    string `json:"user_name"`
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Session{}, fmt.Errorf("decode token exchange: %w", err)
	}
	if out.Status != "success" {
		return Session{}, fmt.Errorf("kite token exchange status %q", out.Status)
	}
	if out.Data.AccessToken == "" {
		return Session{}, fmt.Errorf("kite token exchange returned empty access_token")
	}
	return Session{
		UserID:      out.Data.UserID,
		UserName:    out.Data.UserName,
		AccessToken: out.Data.AccessToken,
	}, nil
}

// Instruments downloads the instrument master for an exchange, for example NSE.
func (c *Client) Instruments(ctx context.Context, exchange string) ([]Instrument, error) {
	path := "/instruments"
	if exchange != "" {
		path += "/" + url.PathEscape(strings.ToUpper(exchange))
	}

	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kite instruments: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("kite instruments: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return ParseInstruments(resp.Body)
}

// HistoricalDaily downloads daily candles for an instrument token.
func (c *Client) HistoricalDaily(
	ctx context.Context,
	instrumentToken int64,
	symbol string,
	from time.Time,
	to time.Time,
) ([]models.Candle, error) {
	values := url.Values{}
	values.Set("from", from.Format("2006-01-02 15:04:05"))
	values.Set("to", to.Format("2006-01-02 15:04:05"))

	path := fmt.Sprintf(
		"/instruments/historical/%d/day?%s",
		instrumentToken,
		values.Encode(),
	)
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kite historical %s: %w", symbol, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("kite historical %s: HTTP %d: %s", symbol, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return ParseHistoricalCandles(resp.Body, symbol)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c.APIKey == "" || c.AccessToken == "" {
		return nil, fmt.Errorf("KITE_API_KEY and KITE_ACCESS_TOKEN are required")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Kite-Version", kiteVersion)
	req.Header.Set("Authorization", "token "+c.APIKey+":"+c.AccessToken)
	return req, nil
}

// ParseInstruments parses Kite's instrument master CSV.
func ParseInstruments(r io.Reader) ([]Instrument, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read instruments header: %w", err)
	}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}

	required := []string{"instrument_token", "tradingsymbol", "name", "instrument_type", "segment", "exchange"}
	for _, col := range required {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("instrument CSV missing column %q", col)
		}
	}

	var out []Instrument
	for line := 2; ; line++ {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		token, err := strconv.ParseInt(row[idx["instrument_token"]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: instrument_token: %w", line, err)
		}
		out = append(out, Instrument{
			InstrumentToken: token,
			TradingSymbol:   strings.ToUpper(strings.TrimSpace(row[idx["tradingsymbol"]])),
			Name:            strings.TrimSpace(row[idx["name"]]),
			InstrumentType:  strings.ToUpper(strings.TrimSpace(row[idx["instrument_type"]])),
			Segment:         strings.ToUpper(strings.TrimSpace(row[idx["segment"]])),
			Exchange:        strings.ToUpper(strings.TrimSpace(row[idx["exchange"]])),
		})
	}
	return out, nil
}

// FindEquityInstrument returns the NSE/BSE equity instrument for a symbol.
func FindEquityInstrument(instruments []Instrument, exchange, symbol string) (Instrument, bool) {
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	symbol = NormalizeSymbol(symbol)
	for _, inst := range instruments {
		if inst.Exchange == exchange &&
			inst.TradingSymbol == symbol &&
			inst.InstrumentType == "EQ" {
			return inst, true
		}
	}
	return Instrument{}, false
}

// NormalizeSymbol strips common exchange prefixes/suffixes and uppercases.
func NormalizeSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if _, after, ok := strings.Cut(symbol, ":"); ok {
		symbol = after
	}
	if before, _, ok := strings.Cut(symbol, "."); ok {
		symbol = before
	}
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// ParseHistoricalCandles parses Kite's historical candle JSON response.
func ParseHistoricalCandles(r io.Reader, symbol string) ([]models.Candle, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Candles [][]any `json:"candles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode historical candles: %w", err)
	}
	if resp.Status != "" && resp.Status != "success" {
		return nil, fmt.Errorf("kite historical status %q", resp.Status)
	}

	sym := NormalizeSymbol(symbol)
	out := make([]models.Candle, 0, len(resp.Data.Candles))
	for i, row := range resp.Data.Candles {
		if len(row) < 6 {
			return nil, fmt.Errorf("candle %d: expected at least 6 fields, got %d", i, len(row))
		}
		ts, err := parseKiteTime(row[0])
		if err != nil {
			return nil, fmt.Errorf("candle %d: timestamp: %w", i, err)
		}
		open, err := number(row[1])
		if err != nil {
			return nil, fmt.Errorf("candle %d: open: %w", i, err)
		}
		high, err := number(row[2])
		if err != nil {
			return nil, fmt.Errorf("candle %d: high: %w", i, err)
		}
		low, err := number(row[3])
		if err != nil {
			return nil, fmt.Errorf("candle %d: low: %w", i, err)
		}
		close_, err := number(row[4])
		if err != nil {
			return nil, fmt.Errorf("candle %d: close: %w", i, err)
		}
		volume, err := number(row[5])
		if err != nil {
			return nil, fmt.Errorf("candle %d: volume: %w", i, err)
		}
		out = append(out, models.Candle{
			Symbol:    sym,
			Timestamp: ts,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close_,
			Volume:    int64(volume),
		})
	}
	return out, nil
}

func parseKiteTime(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("expected string, got %T", v)
	}
	layouts := []string{
		"2006-01-02T15:04:05-0700",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", s)
}

func number(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case string:
		return strconv.ParseFloat(n, 64)
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}
