package api

import (
	"strings"
	"testing"
)

func TestSanitizeLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"open /a/b/c.txt failed", "open …/c.txt failed"},
		{"line one\nline two\t\tx", "line one line two x"},
		{"password=hunter2 next", "password=[redacted] next"},
		{"https://user:pw@host/x", "https://[redacted]@host/x"},
		{"key sk-abcdefghijkl1234", "key [redacted]"},
		{"0123456789abcdef0123456789abcdef0123", "[redacted]"},
		{"plain error", "plain error"},
	} {
		if got := sanitizeLine(tc.in); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
	long := sanitizeLine(strings.Repeat("ก ", 200))
	if n := len([]rune(long)); n != maxLogLine {
		t.Errorf("cap: %d runes", n)
	}
}
