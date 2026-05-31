package loader

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCSV_GoogleFinanceExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ITC.csv")
	csv := `,Date,Open,High,Low,Close,Volume
,5/31/2024 15:30:00,426.75,429.55,424.25,426.45,28214102
,6/3/2024 15:30:00,434,434.9,428.65,430.35,15519148
`
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	candles, err := LoadCSV(path, "ITC")
	if err != nil {
		t.Fatalf("LoadCSV returned error: %v", err)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles, want 2", len(candles))
	}
	if candles[0].Symbol != "ITC" {
		t.Errorf("symbol = %q, want ITC", candles[0].Symbol)
	}
	if candles[0].Open != 426.75 {
		t.Errorf("open = %.2f, want 426.75", candles[0].Open)
	}
	if candles[0].Volume != 28214102 {
		t.Errorf("volume = %d, want 28214102", candles[0].Volume)
	}
}
