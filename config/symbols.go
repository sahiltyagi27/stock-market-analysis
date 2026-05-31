package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadSymbols reads a watchlist file and returns non-empty, non-comment lines.
// Lines beginning with '#' and blank lines are ignored.
func LoadSymbols(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open symbols file %q: %w", path, err)
	}
	defer f.Close()

	var symbols []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		symbols = append(symbols, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read symbols file: %w", err)
	}
	if len(symbols) == 0 {
		return nil, fmt.Errorf("symbols file %q contains no symbols", path)
	}
	return symbols, nil
}
