// lightning.go — Blitzortung real-time lightning detection.
//
// TASK-088: Connects to the public Blitzortung WebSocket (wss://ws8.blitzortung.org)
// to receive global lightning strike data. Strikes are buffered in memory for a
// rolling 30-minute window. LightningRisk() counts strikes within 200 km of a
// city and returns a 0–1 risk score (high risk when >50 strikes/30 min).
//
// The background goroutine is started lazily on first call to LightningRisk.
// Received strikes are persisted to data/lightning/{city}_{hour}.json for
// replay and debugging.
//
// Blitzortung message format (JSON):
//
//	{"time":<ns_timestamp>,"lat":<float>,"lon":<float>,"alt":<int>}
package collectors

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	blitzortungHost   = "ws8.blitzortung.org"
	blitzortungPort   = "443"
	blitzortungPath   = "/"
	strikeWindowMins  = 30
	lightningRadiusKM = 200.0
	highRiskThreshold = 50 // strikes per 30 min within radius
)

// lightningStrike represents one decoded Blitzortung event.
type lightningStrike struct {
	TimeNS int64   `json:"time"` // nanoseconds since epoch
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	At     time.Time
}

// strikeBuffer is a thread-safe rolling buffer of recent strikes.
type strikeBuffer struct {
	mu      sync.RWMutex
	strikes []lightningStrike
}

var (
	globalStrikeBuffer strikeBuffer
	lightningOnce      sync.Once
	lightningDataRoot  = "." // overridable in tests
)

// startLightningCollector launches the background WebSocket consumer once.
func startLightningCollector() {
	go func() {
		for {
			if err := connectAndReceive(); err != nil {
				slog.Warn("lightning: websocket disconnected, reconnecting in 15s", "err", err)
			}
			time.Sleep(15 * time.Second)
		}
	}()
}

// connectAndReceive opens a TLS WebSocket to Blitzortung and streams strikes
// into the global buffer. Returns on disconnect or error.
func connectAndReceive() error {
	conn, err := tls.Dial("tcp", net.JoinHostPort(blitzortungHost, blitzortungPort), &tls.Config{
		ServerName: blitzortungHost,
	})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	// Build WebSocket upgrade key.
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("rand key: %w", err)
	}
	wsKey := base64.StdEncoding.EncodeToString(keyBytes)

	// Send HTTP/1.1 upgrade request.
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Origin: https://www.blitzortung.org\r\n\r\n",
		blitzortungPath, blitzortungHost, wsKey,
	)
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("write upgrade: %w", err)
	}

	// Validate the 101 Switching Protocols response.
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if len(statusLine) < 12 || statusLine[9:12] != "101" {
		return fmt.Errorf("unexpected status: %q", statusLine)
	}
	// Verify Sec-WebSocket-Accept.
	expectedAccept := wsAccept(wsKey)
	gotAccept := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read headers: %w", err)
		}
		if line == "\r\n" {
			break
		}
		if len(line) > 22 && line[:22] == "Sec-WebSocket-Accept: " {
			gotAccept = line[22 : len(line)-2] // trim \r\n
		}
	}
	if gotAccept != expectedAccept {
		return fmt.Errorf("ws accept mismatch: got %q want %q", gotAccept, expectedAccept)
	}

	slog.Info("lightning: connected to Blitzortung WebSocket")

	// Subscribe to global feed (region 0 = worldwide).
	subscribeMsg := wsTextFrame([]byte(`{"west":-180,"east":180,"north":90,"south":-90}`))
	if _, err := conn.Write(subscribeMsg); err != nil {
		return fmt.Errorf("write subscribe: %w", err)
	}

	// Read frames indefinitely.
	for {
		payload, err := readWSFrame(br)
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		if len(payload) == 0 {
			continue
		}
		var s lightningStrike
		if err := json.Unmarshal(payload, &s); err != nil {
			continue // ignore non-strike messages
		}
		if s.Lat == 0 && s.Lon == 0 {
			continue
		}
		s.At = time.Unix(0, s.TimeNS).UTC()
		if s.TimeNS == 0 {
			s.At = time.Now().UTC()
		}

		globalStrikeBuffer.mu.Lock()
		globalStrikeBuffer.strikes = append(globalStrikeBuffer.strikes, s)
		// Prune older than 35 minutes to keep memory bounded.
		cutoff := time.Now().Add(-35 * time.Minute)
		i := 0
		for i < len(globalStrikeBuffer.strikes) && globalStrikeBuffer.strikes[i].At.Before(cutoff) {
			i++
		}
		globalStrikeBuffer.strikes = globalStrikeBuffer.strikes[i:]
		globalStrikeBuffer.mu.Unlock()
	}
}

// wsAccept computes the Sec-WebSocket-Accept value per RFC 6455.
func wsAccept(key string) string {
	const magicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + magicGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// wsTextFrame wraps payload in an unmasked WebSocket text frame (client→server
// should be masked, but Blitzortung's server accepts unmasked).
func wsTextFrame(payload []byte) []byte {
	var buf []byte
	buf = append(buf, 0x81) // FIN + opcode text
	l := len(payload)
	if l <= 125 {
		buf = append(buf, byte(l))
	} else if l <= 65535 {
		buf = append(buf, 126, byte(l>>8), byte(l))
	} else {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(l))
		buf = append(buf, 127)
		buf = append(buf, b...)
	}
	buf = append(buf, payload...)
	return buf
}

// readWSFrame reads one WebSocket frame from br and returns the payload.
func readWSFrame(br *bufio.Reader) ([]byte, error) {
	b0, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	b1, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	masked := (b1 & 0x80) != 0
	payloadLen := int64(b1 & 0x7F)
	switch payloadLen {
	case 126:
		var l uint16
		if err := binary.Read(br, binary.BigEndian, &l); err != nil {
			return nil, err
		}
		payloadLen = int64(l)
	case 127:
		var l uint64
		if err := binary.Read(br, binary.BigEndian, &l); err != nil {
			return nil, err
		}
		payloadLen = int64(l)
	}
	_ = b0 // opcode bits not used; we treat all frames as data
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return nil, err
		}
	}
	if payloadLen > 1<<20 { // ignore oversized frames (>1 MB)
		return nil, fmt.Errorf("ws frame too large: %d bytes", payloadLen)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, nil
}

// haversineKM returns the great-circle distance in km between two points.
func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// CountStrikes30Min returns the number of strikes within radiusKM of
// (lat, lon) recorded in the past 30 minutes.
func CountStrikes30Min(lat, lon, radiusKM float64) int {
	lightningOnce.Do(startLightningCollector)
	cutoff := time.Now().Add(-strikeWindowMins * time.Minute)

	globalStrikeBuffer.mu.RLock()
	defer globalStrikeBuffer.mu.RUnlock()

	count := 0
	for _, s := range globalStrikeBuffer.strikes {
		if s.At.Before(cutoff) {
			continue
		}
		if haversineKM(lat, lon, s.Lat, s.Lon) <= radiusKM {
			count++
		}
	}
	return count
}

// LightningRisk returns a 0–1 risk score for a city based on recent strikes.
//
//	strikes30min < 10  → 0.05  (background noise)
//	strikes30min 10-50 → 0.05 + (strikes-10)/40 * 0.45  (linear ramp to 0.50)
//	strikes30min > 50  → 0.50 + min((strikes-50)/50, 1) * 0.45  (high risk up to 0.95)
func LightningRisk(city string, strikes30min int) float64 {
	switch {
	case strikes30min < 10:
		return 0.05
	case strikes30min <= 50:
		return 0.05 + float64(strikes30min-10)/40.0*0.45
	default:
		extra := math.Min(float64(strikes30min-50)/50.0, 1.0)
		return 0.50 + extra*0.45
	}
}

// GetCityLightningRisk fetches the current lightning risk for a city, starts
// the background collector if needed, persists hourly strike counts, and
// returns the risk score together with the raw strike count.
func GetCityLightningRisk(city, dataRoot string) (risk float64, strikes int) {
	c, ok := weather.Cities[city]
	if !ok {
		return 0, 0
	}
	lightningOnce.Do(startLightningCollector)
	strikes = CountStrikes30Min(c.Lat, c.Lon, lightningRadiusKM)
	risk = LightningRisk(city, strikes)
	saveLightningSnapshot(city, strikes, dataRoot)
	return risk, strikes
}

// lightningSnapshot is the on-disk record written each hour.
type lightningSnapshot struct {
	City      string    `json:"city"`
	Hour      string    `json:"hour"` // "2006-01-02T15"
	Strikes30 int       `json:"strikes_30min"`
	Risk      float64   `json:"risk"`
	SavedAt   time.Time `json:"saved_at"`
}

// saveLightningSnapshot writes strike counts to data/lightning/{city}_{hour}.json.
func saveLightningSnapshot(city string, strikes int, dataRoot string) {
	if dataRoot == "" {
		dataRoot = "."
	}
	dir := filepath.Join(dataRoot, "data", "lightning")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	hour := time.Now().UTC().Format("2006-01-02T15")
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.json", city, hour))
	snap := lightningSnapshot{
		City:      city,
		Hour:      hour,
		Strikes30: strikes,
		Risk:      LightningRisk(city, strikes),
		SavedAt:   time.Now().UTC(),
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("lightning: failed to save snapshot", "path", path, "err", err)
	}
}
