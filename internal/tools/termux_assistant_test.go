package tools

import (
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func TestTermuxAssistantToolsRegisterOnlyInTermux(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "0.119")
	t.Setenv("PREFIX", "/data/data/com.termux/files/usr")
	r := registry.New()
	registerTermuxAssistant(r)
	for _, name := range []string{
		"assistant_device_status", "assistant_listen", "assistant_speak",
		"assistant_notify", "assistant_location", "assistant_clipboard_get",
		"assistant_clipboard_set", "assistant_vibrate", "assistant_torch",
		"assistant_sms_send", "assistant_phone_call", "assistant_camera_photo",
		"assistant_microphone_record", "assistant_contacts", "assistant_sms_list",
		"assistant_call_log",
	} {
		if !r.Exists(name) {
			t.Fatalf("expected %s to be registered", name)
		}
	}
}
