package main

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed mission_control_ui/index.html mission_control_ui/app.css mission_control_ui/app.js
var missionControlAssets embed.FS

func missionControlPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := missionControlAssets.ReadFile("mission_control_ui/index.html")
	if err != nil {
		http.Error(w, "mission control unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func missionControlAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/mission-control/assets/")
	contentType := map[string]string{"app.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8"}[name]
	if contentType == "" {
		http.NotFound(w, r)
		return
	}
	data, err := missionControlAssets.ReadFile("mission_control_ui/" + name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
