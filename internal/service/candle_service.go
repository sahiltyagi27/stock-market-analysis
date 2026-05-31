package service

import (
	"context"
	"fmt"

	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

type CandleService struct {
	store *store.CandleStore
}

func NewCandleService(s *store.CandleStore) *CandleService {
	return &CandleService{store: s}
}

// LoadFromCSV parses a CSV file and bulk-upserts all candles for the given symbol.
func (s *CandleService) LoadFromCSV(ctx context.Context, path, symbol string) (int, error) {
	candles, err := loader.LoadCSV(path, symbol)
	if err != nil {
		return 0, fmt.Errorf("load csv: %w", err)
	}
	if err := s.store.BulkUpsert(ctx, candles); err != nil {
		return 0, fmt.Errorf("store upsert: %w", err)
	}
	return len(candles), nil
}

func (s *CandleService) GetCandles(ctx context.Context, symbol string, f store.CandleFilter) ([]models.Candle, error) {
	return s.store.GetCandles(ctx, symbol, f)
}

func (s *CandleService) GetLatest(ctx context.Context, symbol string) (*models.Candle, error) {
	return s.store.GetLatest(ctx, symbol)
}
