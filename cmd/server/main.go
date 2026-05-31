package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/api"
	"github.com/sahiltyagi27/stock-market-analysis/internal/service"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	// -load flag: load CSV into DB then exit (or keep serving)
	csvPath := flag.String("load", "", "path to OHLCV CSV file to ingest")
	symbol := flag.String("symbol", "", "stock symbol for the CSV (required with -load)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx := context.Background()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	candleStore := store.NewCandleStore(db)
	if err := candleStore.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	candleSvc := service.NewCandleService(candleStore)

	// Optional one-shot CSV load
	if *csvPath != "" {
		if *symbol == "" {
			fmt.Fprintln(os.Stderr, "-symbol is required when using -load")
			os.Exit(1)
		}
		n, err := candleSvc.LoadFromCSV(ctx, *csvPath, *symbol)
		if err != nil {
			log.Fatalf("load csv: %v", err)
		}
		log.Printf("loaded %d candles for %s", n, *symbol)
	}

	handler := api.NewHandler(candleSvc)
	srv := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      handler.Routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("server listening on :%s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}
