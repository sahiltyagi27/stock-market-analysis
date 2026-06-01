// Command kite-token exchanges a Kite request_token for a daily access token.
//
// Usage:
//
//  1. Open the printed login URL and complete Kite login.
//  2. Copy request_token from the redirect URL.
//  3. Run:
//     go run ./cmd/kite-token --request-token <request_token>
//  4. Put the printed KITE_ACCESS_TOKEN in .env.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"

	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
)

func main() {
	requestToken := flag.String("request-token", "", "Kite request_token from the login redirect URL")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.KiteAPIKey == "" {
		log.Fatal("KITE_API_KEY is required")
	}
	if cfg.KiteAPISecret == "" {
		log.Fatal("KITE_API_SECRET is required")
	}
	if *requestToken == "" {
		loginURL := "https://kite.zerodha.com/connect/login?v=3&api_key=" + url.QueryEscape(cfg.KiteAPIKey)
		fmt.Println("Open this URL, complete login, then copy request_token from the redirect URL:")
		fmt.Println(loginURL)
		fmt.Println()
		log.Fatal("--request-token is required after login")
	}

	client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, "")
	session, err := client.ExchangeRequestToken(context.Background(), cfg.KiteAPISecret, *requestToken)
	if err != nil {
		log.Fatalf("kite token exchange: %v", err)
	}

	fmt.Printf("User: %s (%s)\n", session.UserName, session.UserID)
	fmt.Println("Add this to .env for today's session:")
	fmt.Printf("KITE_ACCESS_TOKEN=%s\n", session.AccessToken)
}
