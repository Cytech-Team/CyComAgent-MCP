package main

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed inspector_ui/inspector.html inspector_ui/inspector.css inspector_ui/inspector.js inspector_ui/unified.css inspector_ui/unified.js
var inspectorAssets embed.FS

func inspectorV2Page(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := inspectorAssets.ReadFile("inspector_ui/inspector.html")
	if err != nil {
		http.Error(w, "inspector unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func inspectorV2Asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/inspector/assets/")
	var contentType string
	switch name {
	case "unified.css":
		contentType = "text/css; charset=utf-8"
	case "unified.js":
		contentType = "text/javascript; charset=utf-8"
	case "inspector.css":
		contentType = "text/css; charset=utf-8"
	case "inspector.js":
		contentType = "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := inspectorAssets.ReadFile("inspector_ui/" + name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
