package tools

import (
	"reflect"
	"strings"
	"testing"
)

func TestLabwcWtypeKeyArgs(t *testing.T) {
	tests := []struct {
		key  string
		want []string
	}{
		{"CTRL+Q", []string{"-M", "ctrl", "-k", "q", "-m", "ctrl"}},
		{"Ctrl+Shift+T", []string{"-M", "ctrl", "-M", "shift", "-k", "t", "-m", "shift", "-m", "ctrl"}},
		{"Alt+F4", []string{"-M", "alt", "-k", "F4", "-m", "alt"}},
		{"Super+Left", []string{"-M", "logo", "-k", "Left", "-m", "logo"}},
		{"CTRL+ALT+DELETE", []string{"-M", "ctrl", "-M", "alt", "-k", "Delete", "-m", "alt", "-m", "ctrl"}},
		{"CTRL++", []string{"-M", "ctrl", "-k", "plus", "-m", "ctrl"}},
		{"Enter", []string{"-k", "Return"}},
		{"ESC", []string{"-k", "Escape"}},
		{"CTRL", []string{"-k", "Control_L"}},
		{"F12", []string{"-k", "F12"}},
		{"A", []string{"-k", "a"}},
		{"altgr+1", []string{"-M", "altgr", "-k", "1", "-m", "altgr"}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := labwcWtypeKeyArgs(tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLabwcWtypeRejectsInvalidChordWithoutInjection(t *testing.T) {
	for _, key := range []string{"", " ", "CTRL+", "+Q", "CTRL+CTRL+Q", "CTRL+ALT", "CTRL+Q+E", "Ctrl+-evil", "CTRL+SHIFT+"} {
		t.Run(strings.ReplaceAll(key, "/", "_"), func(t *testing.T) {
			if _, err := labwcWtypeKeyArgs(key); err == nil {
				t.Fatalf("accepted invalid chord %q", key)
			}
		})
	}
}
