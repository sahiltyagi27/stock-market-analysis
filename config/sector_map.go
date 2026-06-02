package config

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
)

// LoadSectorMap reads a symbol -> sector index CSV.
//
// Expected columns:
//
//	symbol,sector_index
//	HDFCBANK,NIFTY BANK
//	TCS,NIFTY IT
//
// Blank lines are ignored. Lines whose first field starts with # are ignored.
// Header names are optional but recommended.
func LoadSectorMap(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sector map %q: %w", path, err)
	}
	defer f.Close()
	return ParseSectorMap(f)
}

func ParseSectorMap(r io.Reader) (map[string]string, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1

	out := map[string]string{}
	line := 0
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if len(row) == 0 {
			continue
		}
		symbol := strings.TrimSpace(row[0])
		if symbol == "" || strings.HasPrefix(symbol, "#") {
			continue
		}
		if len(row) < 2 {
			return nil, fmt.Errorf("line %d: expected symbol,sector_index", line)
		}
		sector := strings.TrimSpace(row[1])
		if strings.EqualFold(symbol, "symbol") && strings.EqualFold(sector, "sector_index") {
			continue
		}
		if sector == "" {
			return nil, fmt.Errorf("line %d: sector_index is empty", line)
		}
		out[kite.NormalizeSymbol(symbol)] = kite.IndexDBSymbol(sector)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sector map contains no mappings")
	}
	return out, nil
}
