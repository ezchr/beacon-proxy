package bridge

import "testing"

func TestTranslationKey(t *testing.T) {
	for in, want := range map[string]string{
		"%disconnect.disconnected":        "disconnect.disconnected",
		"\u00a7e%disconnect.disconnected": "disconnect.disconnected",
		"You were banned":                 "",
		"%d players":                      "",
		"100% sure":                       "",
	} {
		got, ok := translationKey(in)
		if ok != (want != "") || got != want {
			t.Errorf("translationKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if keyFallback("disconnect.disconnected") != "Disconnected by Server" {
		t.Error("fallback")
	}
}
