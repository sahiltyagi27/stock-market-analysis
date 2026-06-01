package kite

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultWSURL   = "wss://ws.kite.trade"
	reconnectDelay = 5 * time.Second
	writeTimeout   = 10 * time.Second
	readTimeout    = 60 * time.Second // reset on every message; heartbeat arrives every ~5s
)

// wsRequest is the JSON envelope for all outbound WebSocket messages.
type wsRequest struct {
	Action string `json:"a"`
	Value  any    `json:"v"`
}

// WSClient manages a persistent Kite WebSocket connection, maintains a
// live tick map for fast lookups, and reconnects automatically on disconnect.
//
// Create with NewWSClient, then call Run (blocking) in a goroutine.
// Read ticks via LatestTick or TickCount from any other goroutine.
type WSClient struct {
	// APIKey and AccessToken authenticate the connection. Set at construction.
	APIKey      string
	AccessToken string
	// BaseURL overrides the default WebSocket endpoint (useful in tests).
	BaseURL string

	tokenSymbol map[uint32]string // immutable after construction

	mu    sync.RWMutex
	ticks map[uint32]Tick
}

// NewWSClient creates a WSClient ready to stream ticks.
// tokenSymbol maps Kite instrument tokens to normalised trading symbols.
func NewWSClient(apiKey, accessToken string, tokenSymbol map[uint32]string) *WSClient {
	return &WSClient{
		APIKey:      apiKey,
		AccessToken: accessToken,
		BaseURL:     defaultWSURL,
		tokenSymbol: tokenSymbol,
		ticks:       make(map[uint32]Tick, len(tokenSymbol)),
	}
}

// Run connects to the Kite WebSocket, subscribes to tokens in full mode, and
// updates the internal tick map on every incoming message.
// It reconnects automatically if the connection drops.
// Run blocks until ctx is cancelled.
func (c *WSClient) Run(ctx context.Context, tokens []uint32) error {
	for {
		err := c.connectAndStream(ctx, tokens)
		if ctx.Err() != nil {
			return nil // clean shutdown
		}
		if err != nil {
			log.Printf("kite ws: %v — reconnecting in %s", err, reconnectDelay)
		}
		select {
		case <-time.After(reconnectDelay):
		case <-ctx.Done():
			return nil
		}
	}
}

// connectAndStream opens one WebSocket connection, subscribes to tokens in
// full mode, and reads messages until the connection drops or ctx is cancelled.
func (c *WSClient) connectAndStream(ctx context.Context, tokens []uint32) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	q := u.Query()
	q.Set("api_key", c.APIKey)
	q.Set("access_token", c.AccessToken)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	log.Printf("kite ws: connected to %s", c.BaseURL)

	if err := c.sendSubscribe(conn, tokens); err != nil {
		return err
	}
	log.Printf("kite ws: subscribed %d tokens in %s mode", len(tokens), ModeFull)

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		switch msgType {
		case websocket.BinaryMessage:
			c.handleBinary(msg)
		case websocket.TextMessage:
			c.handleText(msg)
		}
	}
}

// sendSubscribe sends the subscribe and mode=full commands over conn.
func (c *WSClient) sendSubscribe(conn *websocket.Conn, tokens []uint32) error {
	// JSON requires []any for numeric types to avoid uint32 serialization issues.
	tokAny := make([]any, len(tokens))
	for i, t := range tokens {
		tokAny[i] = t
	}

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteJSON(wsRequest{Action: "subscribe", Value: tokAny}); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteJSON(wsRequest{Action: "mode", Value: []any{ModeFull, tokAny}}); err != nil {
		return fmt.Errorf("send mode: %w", err)
	}
	return nil
}

// handleBinary parses a binary WebSocket frame and updates the tick map.
// A 1-byte heartbeat is silently ignored.
func (c *WSClient) handleBinary(msg []byte) {
	if len(msg) <= 1 {
		return // heartbeat
	}

	ticks, err := ParseTicks(msg, c.tokenSymbol)
	if err != nil {
		log.Printf("kite ws: parse ticks: %v", err)
		return
	}

	c.mu.Lock()
	for _, t := range ticks {
		if t.IsTradable {
			c.ticks[t.InstrumentToken] = t
		}
	}
	c.mu.Unlock()
}

// handleText parses JSON text frames (order updates, errors, broker messages).
func (c *WSClient) handleText(msg []byte) {
	var frame struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &frame); err != nil {
		return
	}
	switch frame.Type {
	case "error":
		log.Printf("kite ws error: %s", frame.Data)
	case "message":
		log.Printf("kite ws message: %s", frame.Data)
	}
}

// LatestTick returns the most recent tick for the given instrument token.
// The second return value is false if no tick has been received yet.
func (c *WSClient) LatestTick(token uint32) (Tick, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.ticks[token]
	return t, ok
}

// TickCount returns the number of instruments with at least one tick received.
func (c *WSClient) TickCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.ticks)
}
