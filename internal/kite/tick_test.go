package kite

import (
	"encoding/binary"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Binary fixture helpers
// ---------------------------------------------------------------------------

func put32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// putI32 encodes a signed int32 as big-endian bytes (same wire format as put32).
func putI32(v int32) []byte { return put32(uint32(v)) }

func put16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// concat joins byte slices.
func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// frame wraps one or more raw packets into a Kite binary WebSocket frame.
func frame(packets ...[]byte) []byte {
	buf := put16(uint16(len(packets)))
	for _, p := range packets {
		buf = append(buf, put16(uint16(len(p)))...)
		buf = append(buf, p...)
	}
	return buf
}

// ltpPkt builds an 8-byte LTP packet.
func ltpPkt(token uint32, ltpPaise int32) []byte {
	return concat(put32(token), putI32(ltpPaise))
}

// quotePkt builds a 44-byte quote packet with prices in paise.
func quotePkt(token uint32, ltp, lastQty, avgPrice, vol, buyQty, sellQty, open, high, low, close_ int32) []byte {
	return concat(
		put32(token),
		putI32(ltp), put32(uint32(lastQty)), putI32(avgPrice),
		put32(uint32(vol)), put32(uint32(buyQty)), put32(uint32(sellQty)),
		putI32(open), putI32(high), putI32(low), putI32(close_),
	)
}

// fullPkt builds a 184-byte full packet (zeroed depth and OI).
func fullPkt(token uint32, ltp, open, high, low, close_ int32, vol uint32, exchangeTS uint32) []byte {
	pkt := make([]byte, 184)
	binary.BigEndian.PutUint32(pkt[0:4], token)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(ltp))
	// pkt[8:12]  last qty — zero
	// pkt[12:16] avg price — zero
	binary.BigEndian.PutUint32(pkt[16:20], vol)
	// pkt[20:28] buy/sell qty — zero
	binary.BigEndian.PutUint32(pkt[28:32], uint32(open))
	binary.BigEndian.PutUint32(pkt[32:36], uint32(high))
	binary.BigEndian.PutUint32(pkt[36:40], uint32(low))
	binary.BigEndian.PutUint32(pkt[40:44], uint32(close_))
	// pkt[44:48] last trade time — zero
	// pkt[48:60] OI fields — zero
	binary.BigEndian.PutUint32(pkt[60:64], exchangeTS)
	// pkt[64:184] depth — zeroed
	return pkt
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

var testTokenSymbol = map[uint32]string{
	408065: "HDFCBANK",
	341249: "RELIANCE",
}

func TestParseTicksHeartbeat(t *testing.T) {
	for _, input := range [][]byte{nil, {0x00}, {}} {
		ticks, err := ParseTicks(input, testTokenSymbol)
		if err != nil {
			t.Errorf("input len %d: unexpected error: %v", len(input), err)
		}
		if ticks != nil {
			t.Errorf("input len %d: expected nil ticks, got %v", len(input), ticks)
		}
	}
}

func TestParseTicksLTP(t *testing.T) {
	// 1500.00 rupees = 150000 paise
	msg := frame(ltpPkt(408065, 150000))

	ticks, err := ParseTicks(msg, testTokenSymbol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks, want 1", len(ticks))
	}

	tk := ticks[0]
	if tk.InstrumentToken != 408065 {
		t.Errorf("InstrumentToken = %d, want 408065", tk.InstrumentToken)
	}
	if tk.Symbol != "HDFCBANK" {
		t.Errorf("Symbol = %q, want HDFCBANK", tk.Symbol)
	}
	if tk.LastPrice != 1500.00 {
		t.Errorf("LastPrice = %.2f, want 1500.00", tk.LastPrice)
	}
	if !tk.IsTradable {
		t.Error("IsTradable should be true for LTP packet")
	}
}

func TestParseTicksQuote(t *testing.T) {
	// Prices in paise: 2000.00, open 1980.00, high 2010.00, low 1975.00, close 1950.00
	msg := frame(quotePkt(341249,
		200000, 50, 199000,   // ltp, lastQty, avgPrice
		5000000, 10000, 8000, // vol, buyQty, sellQty
		198000, 201000, 197500, 195000, // open, high, low, close
	))

	ticks, err := ParseTicks(msg, testTokenSymbol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks, want 1", len(ticks))
	}

	tk := ticks[0]
	if tk.Symbol != "RELIANCE" {
		t.Errorf("Symbol = %q, want RELIANCE", tk.Symbol)
	}
	if tk.LastPrice != 2000.00 {
		t.Errorf("LastPrice = %.2f, want 2000.00", tk.LastPrice)
	}
	if tk.Open != 1980.00 {
		t.Errorf("Open = %.2f, want 1980.00", tk.Open)
	}
	if tk.High != 2010.00 {
		t.Errorf("High = %.2f, want 2010.00", tk.High)
	}
	if tk.Low != 1975.00 {
		t.Errorf("Low = %.2f, want 1975.00", tk.Low)
	}
	if tk.Close != 1950.00 {
		t.Errorf("Close = %.2f, want 1950.00", tk.Close)
	}
	if tk.Volume != 5000000 {
		t.Errorf("Volume = %d, want 5000000", tk.Volume)
	}
}

func TestParseTicksFull(t *testing.T) {
	ts := uint32(1748764800) // fixed Unix timestamp
	msg := frame(fullPkt(408065,
		162550,             // ltp = 1625.50
		160000, 163000, 159000, // open, high, low
		158000,             // close (prev day) = 1580.00
		3500000,            // volume
		ts,
	))

	ticks, err := ParseTicks(msg, testTokenSymbol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks, want 1", len(ticks))
	}

	tk := ticks[0]
	if tk.LastPrice != 1625.50 {
		t.Errorf("LastPrice = %.2f, want 1625.50", tk.LastPrice)
	}
	if tk.Open != 1600.00 {
		t.Errorf("Open = %.2f, want 1600.00", tk.Open)
	}
	if tk.High != 1630.00 {
		t.Errorf("High = %.2f, want 1630.00", tk.High)
	}
	if tk.Low != 1590.00 {
		t.Errorf("Low = %.2f, want 1590.00", tk.Low)
	}
	if tk.Close != 1580.00 {
		t.Errorf("Close = %.2f, want 1580.00", tk.Close)
	}
	if tk.Volume != 3500000 {
		t.Errorf("Volume = %d, want 3500000", tk.Volume)
	}
	wantTime := time.Unix(int64(ts), 0).UTC()
	if !tk.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp = %v, want %v", tk.Timestamp, wantTime)
	}
}

func TestParseTicksMultiplePackets(t *testing.T) {
	msg := frame(
		ltpPkt(408065, 162550),    // HDFCBANK 1625.50
		ltpPkt(341249, 200000),    // RELIANCE 2000.00
	)

	ticks, err := ParseTicks(msg, testTokenSymbol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 2 {
		t.Fatalf("got %d ticks, want 2", len(ticks))
	}
	if ticks[0].Symbol != "HDFCBANK" {
		t.Errorf("ticks[0].Symbol = %q, want HDFCBANK", ticks[0].Symbol)
	}
	if ticks[1].Symbol != "RELIANCE" {
		t.Errorf("ticks[1].Symbol = %q, want RELIANCE", ticks[1].Symbol)
	}
}

func TestParseTicksUnknownToken(t *testing.T) {
	// Token not in tokenSymbol map — Symbol should be empty string, no error.
	msg := frame(ltpPkt(999999, 50000))

	ticks, err := ParseTicks(msg, testTokenSymbol)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks, want 1", len(ticks))
	}
	if ticks[0].Symbol != "" {
		t.Errorf("Symbol = %q, want empty for unknown token", ticks[0].Symbol)
	}
	if ticks[0].LastPrice != 500.00 {
		t.Errorf("LastPrice = %.2f, want 500.00", ticks[0].LastPrice)
	}
}

func TestParseTicksTruncatedLength(t *testing.T) {
	// Frame says 1 packet but payload is missing the length field.
	msg := []byte{0x00, 0x01} // num_packets=1, no length field follows
	_, err := ParseTicks(msg, testTokenSymbol)
	if err == nil {
		t.Error("expected error for truncated message, got nil")
	}
}

func TestParseTicksTruncatedPayload(t *testing.T) {
	// Frame says packet is 44 bytes but only 10 bytes of payload follow.
	msg := concat(
		put16(1),   // 1 packet
		put16(44),  // claimed 44-byte packet
		make([]byte, 10), // only 10 bytes
	)
	_, err := ParseTicks(msg, testTokenSymbol)
	if err == nil {
		t.Error("expected error for truncated payload, got nil")
	}
}

func TestParseTicks_IndexPacket28(t *testing.T) {
	// 28-byte index quote packet.
	pkt := concat(
		put32(256265),    // NIFTY 50 token (example)
		putI32(2450000),  // ltp = 24500.00
		putI32(2460000),  // high
		putI32(2440000),  // low
		putI32(2445000),  // open
		putI32(2430000),  // prev close
		putI32(2000),     // net change
	)
	msg := frame(pkt)
	ticks, err := ParseTicks(msg, map[uint32]string{256265: "NIFTY50"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticks) != 1 {
		t.Fatalf("got %d ticks, want 1", len(ticks))
	}
	if ticks[0].IsTradable {
		t.Error("IsTradable should be false for index packet")
	}
	if ticks[0].LastPrice != 24500.00 {
		t.Errorf("LastPrice = %.2f, want 24500.00", ticks[0].LastPrice)
	}
}
