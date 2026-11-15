package main

import (
	"strings"
	"testing"
)

func TestPrintQR(t *testing.T) {
	var b strings.Builder
	if err := printQR(&b, "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=abcdef0123456789&bot_type=3"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("too few lines: %d", len(lines))
	}
	w := len([]rune(lines[0]))
	for i, l := range lines {
		if len([]rune(l)) != w {
			t.Fatalf("line %d width %d != %d", i, len([]rune(l)), w)
		}
	}
	// Height in rows ≈ width/2 (two modules per row).
	if h := len(lines); h < w/2 || h > w/2+1 {
		t.Errorf("unexpected aspect: %d lines x %d cols", h, w)
	}
	if testing.Verbose() {
		t.Log("\n" + b.String())
	}
}
