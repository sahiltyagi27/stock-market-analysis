// Command kite-token manages the daily Kite Connect access token.
//
// # One-command browser flow (default)
//
//	go run ./cmd/kite-token
//
// Starts a local callback server on --port (default 8765), opens the Kite login
// page in your default browser, waits for the OAuth redirect, exchanges the
// request_token for an access_token, and writes KITE_ACCESS_TOKEN directly into
// .env. One command, no copy-pasting.
//
// One-time setup: in your Kite developer app settings set
//
//	Redirect URL → http://127.0.0.1:8765/callback
//
// # Token validity check
//
//	go run ./cmd/kite-token --check
//
// Calls the Kite profile endpoint with the current token. Exits 0 if valid,
// 1 if expired. Used by scripts/eod.sh before running the expensive sync.
//
// # Manual fallback
//
//	go run ./cmd/kite-token --request-token <token>
//
// Skips the browser server and exchanges the token directly. Useful when the
// redirect URL isn't set to localhost or when running headless.
//
// Required environment variables:
//
//	KITE_API_KEY
//	KITE_API_SECRET   (exchange flows only)
//	KITE_ACCESS_TOKEN (--check only)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
)

func main() {
	requestToken := flag.String("request-token", "", "manual: Kite request_token from the login redirect URL")
	port := flag.Int("port", 8765, "local callback server port (browser flow)")
	check := flag.Bool("check", false, "validate current KITE_ACCESS_TOKEN and exit (0=valid, 1=expired)")
	envFile := flag.String("env", ".env", "path to .env file to read/write")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.KiteAPIKey == "" {
		log.Fatal("KITE_API_KEY is required in .env")
	}

	// ── --check: validate current token cheaply ───────────────────────────────
	if *check {
		if cfg.KiteAccessToken == "" {
			fmt.Fprintln(os.Stderr, "no KITE_ACCESS_TOKEN in .env — run: go run ./cmd/kite-token")
			os.Exit(1)
		}
		client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		profile, err := client.Profile(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "token invalid: %v\n", err)
			fmt.Fprintln(os.Stderr, "Refresh with: go run ./cmd/kite-token")
			os.Exit(1)
		}
		fmt.Printf("token OK — %s (%s)\n", profile.UserName, profile.UserID)
		return
	}

	// ── --request-token: manual exchange ─────────────────────────────────────
	if *requestToken != "" {
		if cfg.KiteAPISecret == "" {
			log.Fatal("KITE_API_SECRET is required")
		}
		client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, "")
		session, err := client.ExchangeRequestToken(context.Background(), cfg.KiteAPISecret, *requestToken)
		if err != nil {
			log.Fatalf("token exchange: %v", err)
		}
		if err := writeEnvToken(*envFile, session.AccessToken); err != nil {
			fmt.Printf("warn: could not update %s: %v\nAdd manually:\n", *envFile, err)
			fmt.Printf("KITE_ACCESS_TOKEN=%s\n", session.AccessToken)
			return
		}
		fmt.Printf("✓ %s updated — user: %s (%s)\n", *envFile, session.UserName, session.UserID)
		return
	}

	// ── Default: local callback server + browser ──────────────────────────────
	if cfg.KiteAPISecret == "" {
		log.Fatal("KITE_API_SECRET is required")
	}

	callbackAddr := fmt.Sprintf("127.0.0.1:%d", *port)
	loginURL := fmt.Sprintf("https://kite.zerodha.com/connect/login?v=3&api_key=%s",
		url.QueryEscape(cfg.KiteAPIKey))

	done := make(chan error, 1)
	mux := http.NewServeMux()
	srv := &http.Server{Addr: callbackAddr, Handler: mux}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		rt := r.URL.Query().Get("request_token")
		status := r.URL.Query().Get("status")
		if status != "success" || rt == "" {
			msg := r.URL.Query().Get("message")
			http.Error(w, "Login failed: "+msg, http.StatusBadRequest)
			done <- fmt.Errorf("login failed: %s", msg)
			return
		}

		client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, "")
		session, err := client.ExchangeRequestToken(r.Context(), cfg.KiteAPISecret, rt)
		if err != nil {
			http.Error(w, "Token exchange failed: "+err.Error(), http.StatusInternalServerError)
			done <- err
			return
		}

		writeErr := writeEnvToken(*envFile, session.AccessToken)
		if writeErr != nil {
			fmt.Fprintf(w, "<h2>Token exchanged but %s update failed</h2><pre>KITE_ACCESS_TOKEN=%s</pre><p>Add it manually.</p>",
				*envFile, session.AccessToken)
		} else {
			fmt.Fprintf(w, "<h2>✓ Logged in as %s (%s)</h2><p>%s updated. You can close this tab.</p>",
				session.UserName, session.UserID, *envFile)
		}
		done <- writeErr
	})

	ln, err := net.Listen("tcp", callbackAddr)
	if err != nil {
		log.Fatalf("cannot bind %s: %v\nTry --port to use a different port", callbackAddr, err)
	}
	go func() { _ = srv.Serve(ln) }()

	fmt.Printf("Callback server ready at http://%s/callback\n", callbackAddr)
	fmt.Printf("One-time setup: set Kite app Redirect URL → http://%s/callback\n\n", callbackAddr)
	fmt.Println("Opening Kite login in browser…")
	if err := openBrowser(loginURL); err != nil {
		fmt.Println("Could not open browser — open this URL manually:")
		fmt.Println(loginURL)
	}

	if err := <-done; err != nil {
		log.Fatalf("token refresh failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// writeEnvToken updates KITE_ACCESS_TOKEN in envFile in-place.
// If the key is absent it is appended. Creates the file when it doesn't exist.
func writeEnvToken(envFile, token string) error {
	raw, err := os.ReadFile(envFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	lines := strings.Split(string(raw), "\n")
	updated := false
	for i, line := range lines {
		if strings.HasPrefix(line, "KITE_ACCESS_TOKEN=") {
			lines[i] = "KITE_ACCESS_TOKEN=" + token
			updated = true
			break
		}
	}
	if !updated {
		// Strip trailing blank lines, append key, restore trailing newline.
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "KITE_ACCESS_TOKEN="+token, "")
	}
	return os.WriteFile(envFile, []byte(strings.Join(lines, "\n")), 0600)
}

func openBrowser(rawURL string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{rawURL}
	case "linux":
		cmd, args = "xdg-open", []string{rawURL}
	default:
		return fmt.Errorf("unsupported OS — open manually: %s", rawURL)
	}
	return exec.Command(cmd, args...).Start()
}
