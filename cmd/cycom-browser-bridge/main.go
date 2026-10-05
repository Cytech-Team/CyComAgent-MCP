package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/browserbridge"
	"github.com/coder/websocket"
)

type response struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

var mu sync.Mutex
var conn *websocket.Conn
var lastSeen time.Time
var pending = map[string]chan response{}

const staleAfter = 45 * time.Second

const maxPending = 64

func invalidateConn(c *websocket.Conn, reason string) {
	mu.Lock()
	if conn == c {
		conn = nil
		lastSeen = time.Time{}
	}
	mu.Unlock()
	_ = c.Close(websocket.StatusNormalClosure, reason)
}

func ext(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"chrome-extension://*"}})
	if e != nil {
		return
	}
	c.SetReadLimit(browserbridge.MaxResponseBytes)
	mu.Lock()
	old := conn
	conn = c
	lastSeen = time.Now()
	mu.Unlock()
	if old != nil && old != c {
		_ = old.Close(websocket.StatusNormalClosure, "replaced")
	}
	defer func() {
		mu.Lock()
		if conn == c {
			conn = nil
			lastSeen = time.Time{}
		}
		mu.Unlock()
	}()
	for {
		_, b, e := c.Read(r.Context())
		if e != nil {
			return
		}
		mu.Lock()
		if conn == c {
			lastSeen = time.Now()
		}
		mu.Unlock()
		var x response
		if json.Unmarshal(b, &x) == nil && x.ID != "" {
			mu.Lock()
			ch := pending[x.ID]
			mu.Unlock()
			if ch != nil {
				select {
				case ch <- x:
				default:
				}
			}
		}
	}
}
func api(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		http.Error(w, "browser-originated requests are not allowed", http.StatusForbidden)
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var q map[string]any
	r.Body = http.MaxBytesReader(w, r.Body, browserbridge.MaxRequestBytes)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(&q); err != nil {
		requestDecodeError(w, err)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		requestDecodeError(w, err)
		return
	}
	if q == nil {
		http.Error(w, "request must be an object", http.StatusBadRequest)
		return
	}
	id := fmt.Sprintf("%x", randomID())
	q["id"] = id
	b, err := json.Marshal(q)
	if err != nil || len(b) > browserbridge.MaxRequestBytes {
		http.Error(w, "encoded request too large", http.StatusRequestEntityTooLarge)
		return
	}
	mu.Lock()
	c := conn
	seen := lastSeen
	if c == nil {
		mu.Unlock()
		http.Error(w, "extension not connected", 503)
		return
	}
	if seen.IsZero() || time.Since(seen) > staleAfter {
		if conn == c {
			conn = nil
			lastSeen = time.Time{}
		}
		mu.Unlock()
		_ = c.Close(websocket.StatusNormalClosure, "stale extension")
		http.Error(w, "extension connection stale; reconnecting", 503)
		return
	}
	if len(pending) >= maxPending {
		mu.Unlock()
		http.Error(w, "too many pending requests", http.StatusServiceUnavailable)
		return
	}
	ch := make(chan response, 1)
	pending[id] = ch
	mu.Unlock()
	defer func() { mu.Lock(); delete(pending, id); mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, b); e != nil {
		invalidateConn(c, "write failed")
		http.Error(w, "extension write failed; action outcome may be unknown: "+e.Error(), 502)
		return
	}
	select {
	case x := <-ch:
		body, err := json.Marshal(x)
		if err != nil || len(body) > browserbridge.MaxResponseBytes {
			http.Error(w, "response too large; action outcome may be unknown", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	case <-ctx.Done():
		invalidateConn(c, "request timeout")
		http.Error(w, "extension timeout; action outcome may be unknown; reconnecting", 504)
	}
}

func randomID() [16]byte {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return id
}

func requestDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "bad json", http.StatusBadRequest)
}

func listenAddr(value string) (string, error) {
	if value == "" {
		return browserbridge.DefaultAddr, nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("invalid browser bridge address: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return "", fmt.Errorf("browser bridge address must use a numeric loopback IP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("browser bridge port must be between 1 and 65535")
	}
	return value, nil
}

func main() {
	http.HandleFunc("/extension", ext)
	http.HandleFunc("/api", api)
	addr, err := listenAddr(os.Getenv("CYCOM_BROWSER_BRIDGE_ADDR"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("CyCom Browser Bridge listening on %s", addr)
	server := &http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
