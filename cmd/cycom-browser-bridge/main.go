package main

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
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
	// DOM state responses can exceed coder/websocket's 32 KiB default.
	// Bound allocations to 4 MiB while allowing full accessibility snapshots.
	c.SetReadLimit(4 << 20)
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
				ch <- x
			}
		}
	}
}
func api(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var q map[string]any
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if q == nil {
		q = map[string]any{}
	}
	if q["action"] == "finish" {
		finishTabsHandler(w, r)
		return
	}
	id := time.Now().Format("150405.000000000")
	q["id"] = id
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
	ch := make(chan response, 1)
	pending[id] = ch
	mu.Unlock()
	defer func() { mu.Lock(); delete(pending, id); mu.Unlock() }()
	b, _ := json.Marshal(q)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, b); e != nil {
		invalidateConn(c, "write failed")
		http.Error(w, e.Error(), 502)
		return
	}
	select {
	case x := <-ch:
		trackBrowserResponse(q, x)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(x)
	case <-ctx.Done():
		invalidateConn(c, "request timeout")
		http.Error(w, "extension timeout; reconnecting", 504)
	}
}
func main() {
	initTabLeases()
	go idleTabSweeper()
	http.HandleFunc("/extension", ext)
	http.HandleFunc("/api", api)
	addr := os.Getenv("CYCOM_BROWSER_BRIDGE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:17373"
	}
	log.Printf("CyCom Browser Bridge listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
