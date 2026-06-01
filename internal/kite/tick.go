package kite

import (
	"encoding/binary"
	"fmt"
	"time"
)

// Streaming modes accepted by the Kite WebSocket API.
const (
	ModeLTP   = "ltp"
	ModeQuote = "quote"
	ModeFull  = "full"
)

// Tick holds the latest market data for one instrument received from the
// Kite WebSocket feed.
type Tick struct {
	InstrumentToken uint32
	Symbol          string
	IsTradable      bool // false for index instruments (NIFTY, SENSEX)

	// Price fields — populated in all modes.
	LastPrice float64

	// OHLC for the current session — populated in quote and full modes.
	Open  float64
	High  float64
	Low   float64
	Close float64 // previous day close (reference price)

	// Trade stats — populated in quote and full modes.
	LastQty  uint32
	AvgPrice float64
	Volume   uint32
	BuyQty   uint32
	SellQty  uint32

	// Extended fields — populated in full mode only.
	LastTradeTime time.Time
	OI            uint32
	OIDayHigh     uint32
	OIDayLow      uint32
	Timestamp     time.Time // exchange timestamp
	Depth         Depth
}

// Depth is the 5-level order book from a full-mode tick.
type Depth struct {
	Buy  [5]DepthItem
	Sell [5]DepthItem
}

// DepthItem is one price level in the order book.
type DepthItem struct {
	Price  float64
	Qty    uint32
	Orders uint16
}

// ParseTicks decodes a binary WebSocket message from Kite into a slice of Ticks.
// tokenSymbol maps instrument tokens to trading symbols; tokens not in the map
// will have an empty Symbol field.
// A nil slice and nil error are returned for heartbeat messages (≤1 byte).
func ParseTicks(data []byte, tokenSymbol map[uint32]string) ([]Tick, error) {
	if len(data) <= 1 {
		return nil, nil // heartbeat
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("message too short (%d bytes)", len(data))
	}

	numPackets := int(binary.BigEndian.Uint16(data[0:2]))
	if numPackets == 0 {
		return nil, nil
	}

	ticks := make([]Tick, 0, numPackets)
	offset := 2

	for i := 0; i < numPackets; i++ {
		if offset+2 > len(data) {
			return ticks, fmt.Errorf("packet %d: truncated length field", i)
		}
		pktLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2

		if pktLen == 0 {
			continue
		}
		if offset+pktLen > len(data) {
			return ticks, fmt.Errorf("packet %d: need %d bytes, only %d remain", i, pktLen, len(data)-offset)
		}

		pkt := data[offset : offset+pktLen]
		offset += pktLen

		tick, err := parsePacket(pkt, tokenSymbol)
		if err != nil {
			return ticks, fmt.Errorf("packet %d: %w", i, err)
		}
		ticks = append(ticks, tick)
	}
	return ticks, nil
}

// parsePacket dispatches to the correct parser based on packet length.
//
// Kite packet sizes:
//
//	  8 bytes → LTP (any mode, just token + last price)
//	 28 bytes → Index quote  (token, ltp, high, low, open, close, change)
//	 32 bytes → Index full   (same + exchange timestamp)
//	 44 bytes → Equity quote (token through previous-close, 11 × int32)
//	184 bytes → Equity full  (quote + OI fields + 10-level depth)
func parsePacket(pkt []byte, tokenSymbol map[uint32]string) (Tick, error) {
	if len(pkt) < 4 {
		return Tick{}, fmt.Errorf("packet too short (%d bytes)", len(pkt))
	}
	token := binary.BigEndian.Uint32(pkt[0:4])
	sym := tokenSymbol[token]

	switch len(pkt) {
	case 8:
		return parseLTPPacket(pkt, token, sym), nil
	case 28, 32:
		return parseIndexPacket(pkt, token, sym), nil
	case 44:
		return parseQuotePacket(pkt, token, sym), nil
	case 184:
		return parseFullPacket(pkt, token, sym), nil
	default:
		// Unknown length — return a best-effort tick with just LTP.
		t := Tick{InstrumentToken: token, Symbol: sym, IsTradable: true}
		if len(pkt) >= 8 {
			t.LastPrice = toPrice(pkt[4:8])
		}
		return t, nil
	}
}

func parseLTPPacket(pkt []byte, token uint32, sym string) Tick {
	return Tick{
		InstrumentToken: token,
		Symbol:          sym,
		IsTradable:      true,
		LastPrice:       toPrice(pkt[4:8]),
	}
}

func parseIndexPacket(pkt []byte, token uint32, sym string) Tick {
	t := Tick{
		InstrumentToken: token,
		Symbol:          sym,
		IsTradable:      false,
		LastPrice:       toPrice(pkt[4:8]),
		High:            toPrice(pkt[8:12]),
		Low:             toPrice(pkt[12:16]),
		Open:            toPrice(pkt[16:20]),
		Close:           toPrice(pkt[20:24]),
		// pkt[24:28] = net price change — not stored
	}
	if len(pkt) >= 32 {
		t.Timestamp = toTime(pkt[28:32])
	}
	return t
}

func parseQuotePacket(pkt []byte, token uint32, sym string) Tick {
	return Tick{
		InstrumentToken: token,
		Symbol:          sym,
		IsTradable:      true,
		LastPrice:       toPrice(pkt[4:8]),
		LastQty:         binary.BigEndian.Uint32(pkt[8:12]),
		AvgPrice:        toPrice(pkt[12:16]),
		Volume:          binary.BigEndian.Uint32(pkt[16:20]),
		BuyQty:          binary.BigEndian.Uint32(pkt[20:24]),
		SellQty:         binary.BigEndian.Uint32(pkt[24:28]),
		Open:            toPrice(pkt[28:32]),
		High:            toPrice(pkt[32:36]),
		Low:             toPrice(pkt[36:40]),
		Close:           toPrice(pkt[40:44]),
	}
}

func parseFullPacket(pkt []byte, token uint32, sym string) Tick {
	t := parseQuotePacket(pkt, token, sym)

	t.LastTradeTime = toTime(pkt[44:48])
	t.OI = binary.BigEndian.Uint32(pkt[48:52])
	t.OIDayHigh = binary.BigEndian.Uint32(pkt[52:56])
	t.OIDayLow = binary.BigEndian.Uint32(pkt[56:60])
	t.Timestamp = toTime(pkt[60:64])

	// Market depth: 5 buy entries then 5 sell entries, each 12 bytes.
	// Layout per entry: 4-byte qty | 4-byte price (paise) | 2-byte orders | 2-byte padding.
	const entrySize = 12
	const depthOffset = 64

	for i := 0; i < 5; i++ {
		base := depthOffset + i*entrySize
		t.Depth.Buy[i] = DepthItem{
			Qty:    binary.BigEndian.Uint32(pkt[base : base+4]),
			Price:  toPrice(pkt[base+4 : base+8]),
			Orders: binary.BigEndian.Uint16(pkt[base+8 : base+10]),
		}
	}
	for i := 0; i < 5; i++ {
		base := depthOffset + 5*entrySize + i*entrySize
		t.Depth.Sell[i] = DepthItem{
			Qty:    binary.BigEndian.Uint32(pkt[base : base+4]),
			Price:  toPrice(pkt[base+4 : base+8]),
			Orders: binary.BigEndian.Uint16(pkt[base+8 : base+10]),
		}
	}
	return t
}

// toPrice converts a 4-byte big-endian int32 (paise) to rupees.
func toPrice(b []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(b))) / 100.0
}

// toTime converts a 4-byte big-endian Unix timestamp to time.Time UTC.
func toTime(b []byte) time.Time {
	sec := int64(binary.BigEndian.Uint32(b))
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
