package tools

import (
	"fmt"
	"strings"
)

// labwcWtypeKeyArgs converts an API key or chord (e.g. CTRL+Q) into
// wtype's modifier-press / key-tap / modifier-release argument sequence.
// wtype -k accepts a SINGLE keysym; it cannot parse chord strings.
// This function is pure and never sends input, so callers can validate first.
func labwcWtypeKeyArgs(key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	parts := strings.Split(key, "+")
	if len(parts) > 8 {
		return nil, fmt.Errorf("too many keys in chord %q", key)
	}
	// A trailing + can mean the literal plus key (CTRL++).
	if len(parts) >= 2 && parts[len(parts)-1] == "" && parts[len(parts)-2] == "" {
		parts = append(parts[:len(parts)-2], "plus")
	}
	modifiers := make([]string, 0, len(parts)-1)
	seen := make(map[string]bool)
	args := make([]string, 0, len(parts)*2+2)
	for i, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			return nil, fmt.Errorf("invalid empty key in chord %q", key)
		}
		normalized := strings.ToLower(token)
		modifier := ""
		switch normalized {
		case "ctrl", "control", "ctl", "control_l", "control_r":
			modifier = "ctrl"
		case "alt", "option", "alt_l", "alt_r":
			modifier = "alt"
		case "shift", "shift_l", "shift_r":
			modifier = "shift"
		case "super", "meta", "win", "windows", "cmd", "command", "logo", "mod4":
			modifier = "logo"
		case "altgr", "alt_graph", "mode_switch":
			modifier = "altgr"
		}
		if i < len(parts)-1 {
			if modifier == "" {
				return nil, fmt.Errorf("%q is not a supported modifier in chord %q", token, key)
			}
			if seen[modifier] {
				return nil, fmt.Errorf("duplicate modifier %q in chord %q", token, key)
			}
			seen[modifier] = true
			modifiers = append(modifiers, modifier)
			args = append(args, "-M", modifier)
			continue
		}
		// A standalone modifier is a single key tap. Chords must end in an
		// actual key rather than leaving modifiers pressed.
		if modifier != "" && len(parts) > 1 {
			return nil, fmt.Errorf("chord %q must end in a non-modifier key", key)
		}
		var keysym string
		switch normalized {
		case "enter", "return", "kp_enter":
			keysym = "Return"
		case "esc", "escape":
			keysym = "Escape"
		case "space", "spacebar":
			keysym = "space"
		case "backspace", "bksp":
			keysym = "BackSpace"
		case "del", "delete":
			keysym = "Delete"
		case "ins", "insert":
			keysym = "Insert"
		case "pgup", "pageup", "prior":
			keysym = "Prior"
		case "pgdn", "pagedown", "next":
			keysym = "Next"
		case "up", "down", "left", "right", "home", "end", "tab":
			keysym = strings.ToUpper(normalized[:1]) + normalized[1:]
		case "printscreen", "prtsc", "print":
			keysym = "Print"
		case "plus":
			keysym = "plus"
		case "minus":
			keysym = "minus"
		default:
			if len(normalized) == 1 {
				keysym = normalized // CTRL+Q must not become CTRL+SHIFT+Q
			} else if len(normalized) >= 2 && len(normalized) <= 3 && normalized[0] == 'f' {
				// F1..F35 are XKB key names; reject invalid names downstream.
				keysym = strings.ToUpper(normalized)
			} else {
				keysym = token // allow other XKB keysyms
			}
		}
		if modifier != "" { // CTRL alone -> Control_L
			switch modifier {
			case "ctrl":
				keysym = "Control_L"
			case "alt":
				keysym = "Alt_L"
			case "shift":
				keysym = "Shift_L"
			case "logo":
				keysym = "Super_L"
			case "altgr":
				keysym = "ISO_Level3_Shift"
			}
		}
		if strings.HasPrefix(keysym, "-") {
			return nil, fmt.Errorf("invalid key name %q", token)
		}
		args = append(args, "-k", keysym)
	}
	for i := len(modifiers) - 1; i >= 0; i-- {
		args = append(args, "-m", modifiers[i])
	}
	return args, nil
}
