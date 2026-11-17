package tools

import "testing"

func TestAndroidKeyEventMapsGenericNames(t *testing.T) {
	cases := map[string]string{
		"enter":     "KEYCODE_ENTER",
		"Return":    "KEYCODE_ENTER",
		"esc":       "KEYCODE_ESCAPE",
		"backspace": "KEYCODE_DEL",
		"up":        "KEYCODE_DPAD_UP",
		// Already-explicit and numeric forms must pass through untouched so
		// callers can always bypass the friendly-name table.
		"KEYCODE_APP_SWITCH": "KEYCODE_APP_SWITCH",
		"187":                "187",
		// Unknown friendly names still produce a usable keycode rather than an
		// error, so a new Android key does not require a code change.
		"app_switch": "KEYCODE_APP_SWITCH",
		"ctrl-a":     "KEYCODE_CTRL_A",
	}
	for in, want := range cases {
		if got := androidKeyEvent(in); got != want {
			t.Errorf("androidKeyEvent(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEscapeAndroidInputTextHandlesShellSensitiveCharacters(t *testing.T) {
	// `input text` splits on spaces and treats % as an escape introducer, so a
	// naive passthrough silently drops or mangles both.
	if got := escapeAndroidInputText("hello world"); got != "hello%sworld" {
		t.Errorf("space escaping: got %q", got)
	}
	if got := escapeAndroidInputText("100%"); got != "100%%" {
		t.Errorf("percent escaping: got %q", got)
	}
	if got := escapeAndroidInputText("a% b"); got != "a%%%sb" {
		t.Errorf("combined escaping: got %q", got)
	}
}

func TestAndroidInputCandidatesRejectUnsupportedActions(t *testing.T) {
	// Android has no hover pointer; failing loudly beats faking motion.
	if _, err := androidInputCandidates(desktopInputRequest{Action: "mouse_move", X: 1, Y: 1}); err == nil {
		t.Error("mouse_move should be rejected on android")
	}
	if _, err := androidInputCandidates(desktopInputRequest{Action: "bogus"}); err == nil {
		t.Error("unknown action should be rejected")
	}
	if _, err := androidInputCandidates(desktopInputRequest{Action: "text"}); err == nil {
		t.Error("empty text should be rejected")
	}
	if _, err := androidInputCandidates(desktopInputRequest{Action: "key"}); err == nil {
		t.Error("empty key should be rejected")
	}
	if _, err := androidInputCandidates(desktopInputRequest{Action: "click", Button: 3}); err == nil {
		t.Error("non-primary click should be rejected")
	}
}
