package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

type termuxAPISpec struct {
	Risk string
}

// Keep this list aligned with the public client scripts shipped by termux/termux-api-package.
// These are Android app APIs usable from an ordinary unrooted Termux process. Individual
// Android permissions and device/OS support still apply.
var termuxAPIAllowlist = map[string]termuxAPISpec{
	"termux-api-start": {"write"}, "termux-api-stop": {"write"},
	"termux-audio-info": {"read"}, "termux-battery-status": {"read"},
	"termux-brightness": {"write"}, "termux-call-log": {"sensitive-read"},
	"termux-camera-info": {"read"}, "termux-camera-photo": {"sensitive-write"},
	"termux-clipboard-get": {"sensitive-read"}, "termux-clipboard-set": {"write"},
	"termux-contact-list": {"sensitive-read"}, "termux-dialog": {"write"},
	"termux-download": {"write"}, "termux-fingerprint": {"sensitive-read"},
	"termux-infrared-frequencies": {"read"}, "termux-infrared-transmit": {"write"},
	"termux-job-scheduler": {"write"}, "termux-keystore": {"sensitive-write"},
	"termux-location": {"sensitive-read"}, "termux-media-player": {"write"},
	"termux-media-scan": {"write"}, "termux-microphone-record": {"sensitive-write"},
	"termux-nfc": {"sensitive-write"}, "termux-notification": {"write"},
	"termux-notification-channel": {"write"}, "termux-notification-list": {"read"},
	"termux-notification-remove": {"write"}, "termux-saf-create": {"write"},
	"termux-saf-dirs": {"read"}, "termux-saf-ls": {"read"},
	"termux-saf-managedir": {"write"}, "termux-saf-mkdir": {"write"},
	"termux-saf-read": {"sensitive-read"}, "termux-saf-rm": {"write"},
	"termux-saf-stat": {"read"}, "termux-saf-write": {"sensitive-write"},
	"termux-sensor": {"read"}, "termux-share": {"write"},
	"termux-sms-inbox": {"sensitive-read"}, "termux-sms-list": {"sensitive-read"},
	"termux-sms-send": {"sensitive-write"}, "termux-speech-to-text": {"sensitive-write"},
	"termux-storage-get": {"sensitive-read"}, "termux-telephony-call": {"sensitive-write"},
	"termux-telephony-cellinfo": {"sensitive-read"}, "termux-telephony-deviceinfo": {"sensitive-read"},
	"termux-toast": {"write"}, "termux-torch": {"write"},
	"termux-tts-engines": {"read"}, "termux-tts-speak": {"write"},
	"termux-usb": {"sensitive-write"}, "termux-vibrate": {"write"},
	"termux-volume": {"write"}, "termux-wallpaper": {"write"},
	"termux-wifi-connectioninfo": {"sensitive-read"}, "termux-wifi-enable": {"write"},
	"termux-wifi-scaninfo": {"sensitive-read"},
}

func registerTermuxAPI(r *registry.Registry, pe *policy.Engine) {
	if !platform.IsTermux() && runtime.GOOS != "android" {
		return
	}
	must(r.Add(registry.Tool{
		Name:        "android_api_list",
		Description: "List supported non-root Termux:API adapters and whether each command is installed on this device. Rooted Android is not supported or planned.",
		InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true},
		Handler: func(context.Context, json.RawMessage) (any, error) { return termuxAPIList(), nil },
	}))
	must(r.Add(registry.Tool{
		Name:        "android_api_call",
		Description: "Call an allowlisted Termux:API command without a shell. Requires the Termux:API app/package and any Android permission needed by that API. This is non-root only.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"command":         registry.String("allowlisted Termux:API command, e.g. termux-battery-status"),
			"args":            registry.StringArray("arguments passed directly to the command"),
			"stdin":           registry.String("optional UTF-8 stdin"),
			"timeout_seconds": registry.Integer("timeout; default 30, maximum 300"),
		}, []string{"command"}), Source: "core",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return termuxAPICall(ctx, raw, pe) },
	}))
}

func termuxAPIList() map[string]any {
	names := make([]string, 0, len(termuxAPIAllowlist))
	for name := range termuxAPIAllowlist {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		path, err := exec.LookPath(name)
		items = append(items, map[string]any{"command": name, "risk": termuxAPIAllowlist[name].Risk, "available": err == nil, "path": path})
	}
	return map[string]any{
		"platform": "android/termux", "root_required": false,
		"android_device_root_support": "not_supported", "android_device_root_planned": false,
		"commands": items,
	}
}

func termuxAPICall(ctx context.Context, raw json.RawMessage, pe *policy.Engine) (any, error) {
	var in struct {
		Command        string   `json:"command"`
		Args           []string `json:"args"`
		Stdin          string   `json:"stdin"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if !platform.IsTermux() && runtime.GOOS != "android" {
		return nil, fmt.Errorf("android_api_call is only available on Android/Termux")
	}
	spec, ok := termuxAPIAllowlist[in.Command]
	if !ok {
		return nil, fmt.Errorf("Termux:API command %q is not allowlisted", in.Command)
	}
	if strings.HasPrefix(spec.Risk, "sensitive-") && pe != nil && !pe.Snapshot().AllowSensitiveAndroid {
		return nil, fmt.Errorf("policy denied sensitive Android API command %s; set allow_sensitive_android=true to opt in", in.Command)
	}
	bin, err := exec.LookPath(in.Command)
	if err != nil {
		return nil, fmt.Errorf("%s is unavailable; install the termux-api package and matching Termux:API app: %w", in.Command, err)
	}
	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, in.Args...)
	if in.Stdin != "" {
		cmd.Stdin = strings.NewReader(in.Stdin)
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = 2<<20, 2<<20
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err = cmd.Run()
	exit := 0
	timedOut := cctx.Err() == context.DeadlineExceeded
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if timedOut {
			exit = -1
		} else {
			return nil, err
		}
	}
	return map[string]any{
		"command": in.Command, "risk": spec.Risk, "exit_code": exit,
		"stdout": stdout.String(), "stderr": stderr.String(),
		"timed_out": timedOut, "truncated": stdout.truncated || stderr.truncated,
		"duration_ms": time.Since(start).Milliseconds(), "root_used": false,
	}, nil
}
