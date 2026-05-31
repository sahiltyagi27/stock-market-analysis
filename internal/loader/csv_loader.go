// Package loader parses OHLCV data from CSV files into Candle records.
// Supported date formats: YYYY-MM-DD, MM/DD/YYYY, RFC3339.
// Required columns (case-insensitive): date, open, high, low, close, volume.
package loader

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Expected CSV columns (case-insensitive): date,open,high,low,close,volume
// Date formats tried: 2006-01-02, 01/02/2006, 2006-01-02 15:04:05

var dateFormats = []string{
	"2006-01-02",
	"01/02/2006",
	"2006-01-02 15:04:05",
	time.RFC3339,
}

func LoadCSV(path, symbol string) ([]models.Candle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return parseCSV(f, symbol)
}

func parseCSV(r io.Reader, symbol string) ([]models.Candle, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx, err := columnIndex(header)
	if err != nil {
		return nil, err
	}

	sym := strings.ToUpper(symbol)
	var candles []models.Candle

	for lineNum := 2; ; lineNum++ {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		ts, err := parseDate(row[idx["date"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: date %q: %w", lineNum, row[idx["date"]], err)
		}

		open, err := parseFloat(row[idx["open"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: open: %w", lineNum, err)
		}
		high, err := parseFloat(row[idx["high"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: high: %w", lineNum, err)
		}
		low, err := parseFloat(row[idx["low"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: low: %w", lineNum, err)
		}
		close_, err := parseFloat(row[idx["close"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: close: %w", lineNum, err)
		}
		volume, err := parseInt(row[idx["volume"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: volume: %w", lineNum, err)
		}

		candles = append(candles, models.Candle{
			Symbol:    sym,
			Timestamp: ts,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close_,
			Volume:    volume,
		})
	}
	return candles, nil
}

func columnIndex(header []string) (map[string]int, error) {
	required := []string{"date", "open", "high", "low", "close", "volume"}
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, col := range required {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("missing required column %q in CSV header", col)
		}
	}
	return idx, nil
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range dateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date format")
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func parseInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	return strconv.ParseInt(s, 10, 64)
}
