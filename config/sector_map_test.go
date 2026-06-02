package config_test

import (
	"strings"
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
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

func TestCommittedSectorMap(t *testing.T) {
	got, err := config.LoadSectorMap("sector-map.csv")
	if err != nil {
		t.Fatalf("LoadSectorMap returned error: %v", err)
	}
	if len(got) != 360 {
		t.Fatalf("mapping count = %d, want 360", len(got))
	}

	symbols, err := config.LoadSymbols("symbols.txt")
	if err != nil {
		t.Fatalf("LoadSymbols returned error: %v", err)
	}
	symbolSet := map[string]bool{}
	for _, symbol := range symbols {
		symbolSet[symbol] = true
	}

	supported := map[string]bool{}
	for _, name := range kite.SectorIndexNames {
		supported[kite.IndexDBSymbol(name)] = true
	}

	for symbol, sector := range got {
		if !symbolSet[symbol] {
			t.Errorf("sector map contains %s, which is not in symbols.txt", symbol)
		}
		if !supported[sector] {
			t.Errorf("sector map contains unsupported sector %s for %s", sector, symbol)
		}
	}
}
