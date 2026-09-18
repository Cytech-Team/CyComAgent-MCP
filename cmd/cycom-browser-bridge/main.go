package main

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"log"
	"net/http"
	"sync"
	"time"
)

type request struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	TabID    int    `json:"tabId,omitempty"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Index    *int   `json:"index,omitempty"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	Active   *bool  `json:"active,omitempty"`
}
type response struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

var mu sync.Mutex
var conn *websocket.Conn
var pending = map[string]chan response{}

func ext(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"chrome-extension://*"}})
	if e != nil {
		return
	}
	mu.Lock()
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "replaced")
	}
	conn = c
	mu.Unlock()
	defer func() {
		mu.Lock()
		if conn == c {
			conn = nil
		}
		mu.Unlock()
	}()
	for {
		_, b, e := c.Read(r.Context())
		if e != nil {
			return
		}
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
	var q request
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	q.ID = time.Now().Format("150405.000000000")
	mu.Lock()
	c := conn
	if c == nil {
		mu.Unlock()
		http.Error(w, "extension not connected", 503)
		return
	}
	ch := make(chan response, 1)
	pending[q.ID] = ch
	mu.Unlock()
	defer func() { mu.Lock(); delete(pending, q.ID); mu.Unlock() }()
	b, _ := json.Marshal(q)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, b); e != nil {
		http.Error(w, e.Error(), 502)
		return
	}
	select {
	case x := <-ch:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(x)
	case <-ctx.Done():
		http.Error(w, "extension timeout", 504)
	}
}
func main() {
	http.HandleFunc("/extension", ext)
	http.HandleFunc("/api", api)
	log.Println("CyCom Browser Bridge listening on 127.0.0.1:17373")
	log.Fatal(http.ListenAndServe("127.0.0.1:17373", nil))
}
