package tools

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLabwcDesktopExtentFromJSON(t *testing.T) {
	data := []byte(`[
		{"enabled":true,"modes":[{"width":2560,"height":1440,"current":true}],"position":{"x":1920,"y":0},"transform":"normal","scale":1},
		{"enabled":true,"modes":[{"width":1920,"height":1080,"current":true}],"position":{"x":0,"y":205},"transform":"normal","scale":1}
	]`)
	width, height, err := labwcDesktopExtentFromJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if width != 4480 || height != 1440 {
		t.Fatalf("extent=%dx%d want 4480x1440", width, height)
	}
}

func TestLabwcDesktopExtentRespectsScaleAndRotation(t *testing.T) {
	data := []byte(`[
		{"enabled":true,"modes":[{"width":3000,"height":2000,"current":true}],"position":{"x":0,"y":0},"transform":"90","scale":2}
	]`)
	width, height, err := labwcDesktopExtentFromJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if width != 1000 || height != 1500 {
		t.Fatalf("extent=%dx%d want 1000x1500", width, height)
	}
}

func TestSendLabwcPointerCommandUsesDedicatedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pointer.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		received <- string(buf[:n])
		_, _ = conn.Write([]byte("OK\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := sendLabwcPointerCommand(ctx, []string{"CYCOM_LABWC_POINTER_SOCKET=" + path}, "click 1")
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)
	if got["adapter"] != "labwc-wlr-pointer" || got["action"] != "click" {
		t.Fatalf("unexpected result: %#v", got)
	}
	select {
	case line := <-received:
		if line != "click 1\n" {
			t.Fatalf("command=%q want click 1\\n", line)
		}
	case <-ctx.Done():
		t.Fatal("pointer command was not received")
	}
}
