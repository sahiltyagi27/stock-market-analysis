package config_test

import (
	"strings"
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/config"
)

func TestParseSectorMap(t *testing.T) {
	body := `
# comment
symbol,sector_index
NSE:HDFCBANK,NIFTY BANK
TCS,NIFTY IT
TATASTEEL.NS,NIFTY METAL
`
	got, err := config.ParseSectorMap(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseSectorMap returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("mapping count = %d, want 3: %#v", len(got), got)
	}
	cases := map[string]string{
		"HDFCBANK":  "NIFTYBANK",
		"TCS":       "NIFTYIT",
		"TATASTEEL": "NIFTYMETAL",
	}
	for symbol, want := range cases {
		if got[symbol] != want {
			t.Errorf("sector[%s] = %q, want %q", symbol, got[symbol], want)
		}
	}
}

func TestParseSectorMap_EmptyErrors(t *testing.T) {
	_, err := config.ParseSectorMap(strings.NewReader("# only comments\n"))
	if err == nil {
		t.Fatal("expected empty map error")
	}
}
