package config_test

import (
	"os"
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/config"
)

func writeSymbolsFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "symbols*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestLoadSymbols_BasicParsing(t *testing.T) {
	path := writeSymbolsFile(t, "HDFCBANK.NS\nRELIANCE.NS\nTCS.NS\n")
	symbols, err := config.LoadSymbols(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(symbols) != 3 {
		t.Errorf("got %d symbols, want 3", len(symbols))
	}
	if symbols[0] != "HDFCBANK.NS" {
		t.Errorf("symbols[0] = %q, want HDFCBANK.NS", symbols[0])
	}
}

func TestLoadSymbols_IgnoresComments(t *testing.T) {
	path := writeSymbolsFile(t, "# comment\nHDFCBANK.NS\n# another comment\nTCS.NS\n")
	symbols, err := config.LoadSymbols(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(symbols) != 2 {
		t.Errorf("got %d symbols, want 2 (comments must be skipped)", len(symbols))
	}
}

func TestLoadSymbols_IgnoresBlankLines(t *testing.T) {
	path := writeSymbolsFile(t, "\nHDFCBANK.NS\n\n\nTCS.NS\n\n")
	symbols, err := config.LoadSymbols(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(symbols) != 2 {
		t.Errorf("got %d symbols, want 2 (blank lines must be skipped)", len(symbols))
	}
}

func TestLoadSymbols_TrimsWhitespace(t *testing.T) {
	path := writeSymbolsFile(t, "  HDFCBANK.NS  \n  TCS.NS\n")
	symbols, err := config.LoadSymbols(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if symbols[0] != "HDFCBANK.NS" {
		t.Errorf("symbols[0] = %q, expected trimmed HDFCBANK.NS", symbols[0])
	}
}

func TestLoadSymbols_EmptyFileErrors(t *testing.T) {
	path := writeSymbolsFile(t, "# only comments\n\n")
	_, err := config.LoadSymbols(path)
	if err == nil {
		t.Error("expected error for empty symbol list, got nil")
	}
}

func TestLoadSymbols_FileNotFoundErrors(t *testing.T) {
	_, err := config.LoadSymbols("/nonexistent/path/symbols.txt")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
