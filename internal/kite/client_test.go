package kite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExchangeRequestToken(t *testing.T) {
	var sawChecksum bool
	wantChecksum := sha256.Sum256([]byte("api-key" + "request-token" + "api-secret"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/token" {
			t.Fatalf("path = %q, want /session/token", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("api_key") != "api-key" {
			t.Errorf("api_key = %q, want api-key", r.Form.Get("api_key"))
		}
		if r.Form.Get("request_token") != "request-token" {
			t.Errorf("request_token = %q, want request-token", r.Form.Get("request_token"))
		}
		if r.Form.Get("checksum") == fmt.Sprintf("%x", wantChecksum) {
			sawChecksum = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"user_id":"AB1234","user_name":"Test User","access_token":"access-token"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "api-key", "")
	session, err := c.ExchangeRequestToken(context.Background(), "api-secret", "request-token")
	if err != nil {
		t.Fatalf("ExchangeRequestToken returned error: %v", err)
	}
	if !sawChecksum {
		t.Error("expected checksum to be sent")
	}
	if session.AccessToken != "access-token" {
		t.Errorf("AccessToken = %q, want access-token", session.AccessToken)
	}
}

func TestParseInstrumentsAndFindEquityInstrument(t *testing.T) {
	csv := `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange
424961,1660,ITC,ITC LTD,0,,,0.05,1,EQ,NSE,NSE
123,1,ITC25JUNFUT,,0,2025-06-26,,0.05,1600,FUT,NFO-FUT,NFO
`
	instruments, err := ParseInstruments(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseInstruments returned error: %v", err)
	}
	got, ok := FindEquityInstrument(instruments, "NSE", "NSE:ITC")
	if !ok {
		t.Fatal("expected ITC equity instrument")
	}
	if got.InstrumentToken != 424961 {
		t.Errorf("InstrumentToken = %d, want 424961", got.InstrumentToken)
	}
}

func TestFindInstrumentByName_MatchesIndexByTradingSymbolOrName(t *testing.T) {
	csv := `instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange
256265,1001,NIFTY 50,NIFTY 50,0,,,0.05,1,EQ,INDICES,NSE
260105,1002,NIFTY BANK,NIFTY BANK,0,,,0.05,1,EQ,INDICES,NSE
261001,1003,NIFTYIT,NIFTY IT,0,,,0.05,1,EQ,INDICES,NSE
`
	instruments, err := ParseInstruments(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseInstruments returned error: %v", err)
	}
	got, ok := FindInstrumentByName(instruments, "NSE", "NIFTY BANK")
	if !ok {
		t.Fatal("expected NIFTY BANK instrument")
	}
	if got.InstrumentToken != 260105 {
		t.Errorf("InstrumentToken = %d, want 260105", got.InstrumentToken)
	}

	got, ok = FindInstrumentByName(instruments, "NSE", "nifty it")
	if !ok {
		t.Fatal("expected NIFTY IT instrument by display name")
	}
	if got.InstrumentToken != 261001 {
		t.Errorf("InstrumentToken = %d, want 261001", got.InstrumentToken)
	}
}

func TestParseHistoricalCandles(t *testing.T) {
	body := `{
		"status": "success",
		"data": {
			"candles": [
				["2026-05-29T00:00:00+0530", 292, 292.4, 285.9, 286.9, 31930439]
			]
		}
	}`
	candles, err := ParseHistoricalCandles(strings.NewReader(body), "NSE:ITC")
	if err != nil {
		t.Fatalf("ParseHistoricalCandles returned error: %v", err)
	}
	if len(candles) != 1 {
		t.Fatalf("got %d candles, want 1", len(candles))
	}
	c := candles[0]
	if c.Symbol != "ITC" {
		t.Errorf("Symbol = %q, want ITC", c.Symbol)
	}
	if c.Close != 286.9 {
		t.Errorf("Close = %.2f, want 286.90", c.Close)
	}
	if c.Volume != 31930439 {
		t.Errorf("Volume = %d, want 31930439", c.Volume)
	}
}

func TestNormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"NSE:ITC":     "ITC",
		"HDFCBANK.NS": "HDFCBANK",
		" tcs ":       "TCS",
	}
	for input, want := range cases {
		if got := NormalizeSymbol(input); got != want {
			t.Errorf("NormalizeSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIndexDBSymbol(t *testing.T) {
	cases := map[string]string{
		"NIFTY 50":          "NIFTY50",
		"NIFTY BANK":        "NIFTYBANK",
		"NIFTY FIN SERVICE": "NIFTYFINSERVICE",
		" nifty it ":        "NIFTYIT",
	}
	for input, want := range cases {
		if got := IndexDBSymbol(input); got != want {
			t.Errorf("IndexDBSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}
