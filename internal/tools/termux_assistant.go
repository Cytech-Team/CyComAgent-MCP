package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

type assistantCommandResult struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

func registerTermuxAssistant(r *registry.Registry) {
	if !platform.IsTermux() {
		return
	}

	readOnly := map[string]any{"readOnlyHint": true}
	must(r.Add(registry.Tool{Name: "assistant_device_status", Description: "Get an assistant-friendly snapshot of Android battery, Wi-Fi and audio state using non-root Termux:API adapters when available.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: assistantDeviceStatus}))
	must(r.Add(registry.Tool{Name: "assistant_listen", Description: "Listen once using Android speech-to-text and return recognized text. Requires microphone permission and Termux:API; no device root is used.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Handler: assistantListen}))
	must(r.Add(registry.Tool{Name: "assistant_speak", Description: "Speak text using Android text-to-speech, similar to a voice assistant response.", InputSchema: registry.ObjectSchema(map[string]any{"text": registry.String("text to speak"), "language": registry.String("optional language code"), "rate": registry.String("optional speech rate accepted by Termux:API"), "pitch": registry.String("optional pitch accepted by Termux:API")}, []string{"text"}), Source: "core", Handler: assistantSpeak}))
	must(r.Add(registry.Tool{Name: "assistant_notify", Description: "Show an Android notification from the assistant.", InputSchema: registry.ObjectSchema(map[string]any{"title": registry.String("notification title"), "content": registry.String("notification content"), "id": registry.String("optional stable notification id")}, []string{"content"}), Source: "core", Handler: assistantNotify}))
	must(r.Add(registry.Tool{Name: "assistant_location", Description: "Get Android device location through Termux:API. Requires Android location permission.", InputSchema: registry.ObjectSchema(map[string]any{"provider": map[string]any{"type": "string", "enum": []string{"gps", "network", "passive"}}, "request": map[string]any{"type": "string", "enum": []string{"once", "last"}}}, nil), Source: "core", Annotations: readOnly, Handler: assistantLocation}))
	must(r.Add(registry.Tool{Name: "assistant_clipboard_get", Description: "Read Android clipboard text through Termux:API.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: assistantClipboardGet}))
	must(r.Add(registry.Tool{Name: "assistant_clipboard_set", Description: "Set Android clipboard text through Termux:API.", InputSchema: registry.ObjectSchema(map[string]any{"text": registry.String("clipboard text")}, []string{"text"}), Source: "core", Handler: assistantClipboardSet}))
	must(r.Add(registry.Tool{Name: "assistant_vibrate", Description: "Vibrate the Android device for a bounded duration.", InputSchema: registry.ObjectSchema(map[string]any{"duration_ms": registry.Integer("duration in milliseconds, 1-10000")}, nil), Source: "core", Handler: assistantVibrate}))
	must(r.Add(registry.Tool{Name: "assistant_torch", Description: "Turn the Android flashlight on or off.", InputSchema: registry.ObjectSchema(map[string]any{"enabled": registry.Boolean("true to turn torch on, false to turn it off")}, []string{"enabled"}), Source: "core", Handler: assistantTorch}))

	// Sensitive assistant capabilities are registered but policy-gated. They remain
	// unavailable until allow_sensitive_android=true and Android grants permission.
	must(r.Add(registry.Tool{Name: "assistant_sms_send", Description: "Send an SMS through Android. Sensitive capability: requires policy opt-in and Android SMS permissions.", InputSchema: registry.ObjectSchema(map[string]any{"numbers": registry.StringArray("recipient phone numbers"), "text": registry.String("message text"), "sim_slot": registry.Integer("optional SIM slot")}, []string{"numbers", "text"}), Source: "core", Handler: assistantSMSSend}))
	must(r.Add(registry.Tool{Name: "assistant_phone_call", Description: "Place an Android phone call. Sensitive capability: requires policy opt-in and CALL_PHONE permission.", InputSchema: registry.ObjectSchema(map[string]any{"number": registry.String("phone number")}, []string{"number"}), Source: "core", Handler: assistantPhoneCall}))
	must(r.Add(registry.Tool{Name: "assistant_camera_photo", Description: "Take a photo with an Android camera and save it to a requested path. Sensitive capability: requires policy opt-in and camera permission.", InputSchema: registry.ObjectSchema(map[string]any{"path": registry.String("output image path"), "camera_id": registry.Integer("camera id, usually 0 rear or 1 front")}, []string{"path"}), Source: "core", Handler: assistantCameraPhoto}))
	must(r.Add(registry.Tool{Name: "assistant_microphone_record", Description: "Start or stop Android microphone recording. Sensitive capability: requires policy opt-in and microphone permission.", InputSchema: registry.ObjectSchema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"start", "stop", "info"}}, "path": registry.String("output file path for start"), "limit_seconds": registry.Integer("recording limit in seconds; 0 means Termux:API default/unlimited behavior")}, []string{"action"}), Source: "core", Handler: assistantMicrophoneRecord}))
	must(r.Add(registry.Tool{Name: "assistant_contacts", Description: "List Android contacts. Sensitive capability: requires policy opt-in and contacts permission.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: assistantContacts}))
	must(r.Add(registry.Tool{Name: "assistant_sms_list", Description: "Read Android SMS messages. Sensitive capability: requires policy opt-in and SMS permissions.", InputSchema: registry.ObjectSchema(map[string]any{"limit": registry.Integer("maximum messages, default 10, max 100"), "type": map[string]any{"type": "string", "enum": []string{"all", "inbox", "sent", "draft", "outbox"}}}, nil), Source: "core", Annotations: readOnly, Handler: assistantSMSList}))
	must(r.Add(registry.Tool{Name: "assistant_call_log", Description: "Read Android call log. Sensitive capability: requires policy opt-in and phone permissions.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: assistantCallLog}))
}

func assistantRun(ctx context.Context, command string, args []string, stdin string, timeout time.Duration) (assistantCommandResult, error) {
	var out assistantCommandResult
	out.Command = command
	bin, err := exec.LookPath(command)
	if err != nil {
		return out, fmt.Errorf("%s is unavailable; install pkg termux-api and the matching Termux:API app: %w", command, err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = 2<<20, 2<<20
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err = cmd.Run()
	out.DurationMS = time.Since(start).Milliseconds()
	out.Stdout, out.Stderr = stdout.String(), stderr.String()
	out.Truncated = stdout.truncated || stderr.truncated
	out.TimedOut = cctx.Err() == context.DeadlineExceeded
	if err == nil {
		return out, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		out.ExitCode = ee.ExitCode()
		return out, nil
	}
	if out.TimedOut {
		out.ExitCode = -1
		return out, nil
	}
	return out, err
}

func parsedAssistantResult(res assistantCommandResult) any {
	var v any
	if strings.TrimSpace(res.Stdout) != "" && json.Unmarshal([]byte(res.Stdout), &v) == nil {
		return map[string]any{"result": v, "command": res.Command, "exit_code": res.ExitCode, "duration_ms": res.DurationMS}
	}
	return res
}

func assistantDeviceStatus(ctx context.Context, _ json.RawMessage) (any, error) {
	commands := []string{"termux-battery-status", "termux-wifi-connectioninfo", "termux-audio-info"}
	out := map[string]any{"platform": "android/termux", "device_root": false}
	for _, cmd := range commands {
		key := strings.TrimPrefix(cmd, "termux-")
		res, err := assistantRun(ctx, cmd, nil, "", 15*time.Second)
		if err != nil {
			out[key] = map[string]any{"available": false, "error": err.Error()}
			continue
		}
		out[key] = parsedAssistantResult(res)
	}
	return out, nil
}

func assistantListen(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := assistantRun(ctx, "termux-speech-to-text", nil, "", 90*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}

func assistantSpeak(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct{ Text, Language, Rate, Pitch string }
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	args := []string{}
	if in.Language != "" {
		args = append(args, "-l", in.Language)
	}
	if in.Rate != "" {
		args = append(args, "-r", in.Rate)
	}
	if in.Pitch != "" {
		args = append(args, "-p", in.Pitch)
	}
	args = append(args, in.Text)
	res, err := assistantRun(ctx, "termux-tts-speak", args, "", 90*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantNotify(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct{ Title, Content, ID string }
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}
	args := []string{"--content", in.Content}
	if in.Title != "" {
		args = append(args, "--title", in.Title)
	}
	if in.ID != "" {
		args = append(args, "--id", in.ID)
	}
	res, err := assistantRun(ctx, "termux-notification", args, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantLocation(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct{ Provider, Request string }
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Provider == "" {
		in.Provider = "gps"
	}
	if in.Request == "" {
		in.Request = "once"
	}
	res, err := assistantRun(ctx, "termux-location", []string{"-p", in.Provider, "-r", in.Request}, "", 90*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}

func assistantClipboardGet(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := assistantRun(ctx, "termux-clipboard-get", nil, "", 15*time.Second)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": strings.TrimSuffix(res.Stdout, "\n"), "exit_code": res.ExitCode, "duration_ms": res.DurationMS}, nil
}

func assistantClipboardSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct{ Text string }
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	res, err := assistantRun(ctx, "termux-clipboard-set", nil, in.Text, 15*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantVibrate(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		DurationMS int `json:"duration_ms"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.DurationMS <= 0 {
		in.DurationMS = 500
	}
	if in.DurationMS > 10000 {
		in.DurationMS = 10000
	}
	res, err := assistantRun(ctx, "termux-vibrate", []string{"-d", strconv.Itoa(in.DurationMS)}, "", 15*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantTorch(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	state := "off"
	if in.Enabled {
		state = "on"
	}
	res, err := assistantRun(ctx, "termux-torch", []string{state}, "", 15*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantSMSSend(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Numbers []string `json:"numbers"`
		Text    string   `json:"text"`
		SIMSlot int      `json:"sim_slot"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if len(in.Numbers) == 0 || strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("numbers and text are required")
	}
	args := []string{"-n", strings.Join(in.Numbers, ",")}
	if in.SIMSlot > 0 {
		args = append(args, "-s", strconv.Itoa(in.SIMSlot))
	}
	args = append(args, in.Text)
	res, err := assistantRun(ctx, "termux-sms-send", args, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantPhoneCall(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Number string `json:"number"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Number) == "" {
		return nil, fmt.Errorf("number is required")
	}
	res, err := assistantRun(ctx, "termux-telephony-call", []string{in.Number}, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func assistantCameraPhoto(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Path     string `json:"path"`
		CameraID int    `json:"camera_id"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	args := []string{}
	if in.CameraID != 0 {
		args = append(args, "-c", strconv.Itoa(in.CameraID))
	}
	args = append(args, in.Path)
	res, err := assistantRun(ctx, "termux-camera-photo", args, "", 60*time.Second)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": in.Path, "camera_id": in.CameraID, "result": res}, nil
}

func assistantMicrophoneRecord(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Action, Path string
		LimitSeconds int `json:"limit_seconds"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	args := []string{}
	switch in.Action {
	case "start":
		if in.Path != "" {
			args = append(args, "-f", in.Path)
		} else {
			args = append(args, "-d")
		}
		if in.LimitSeconds > 0 {
			if in.LimitSeconds > 3600 {
				in.LimitSeconds = 3600
			}
			args = append(args, "-l", strconv.Itoa(in.LimitSeconds))
		}
	case "stop":
		args = []string{"-q"}
	case "info":
		args = []string{"-i"}
	default:
		return nil, fmt.Errorf("action must be start, stop, or info")
	}
	res, err := assistantRun(ctx, "termux-microphone-record", args, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}

func assistantContacts(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := assistantRun(ctx, "termux-contact-list", nil, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}

func assistantSMSList(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Limit int    `json:"limit"`
		Type  string `json:"type"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Type == "" {
		in.Type = "inbox"
	}
	res, err := assistantRun(ctx, "termux-sms-list", []string{"-l", strconv.Itoa(in.Limit), "-t", in.Type, "-d", "-n"}, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}

func assistantCallLog(ctx context.Context, _ json.RawMessage) (any, error) {
	res, err := assistantRun(ctx, "termux-call-log", nil, "", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return parsedAssistantResult(res), nil
}
