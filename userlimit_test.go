package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpeedTextAndParse(t *testing.T) {
	cases := map[string]string{"1": "1MB", "0.5": "500KB", "500kb": "500KB", "100mbps": "100Mbps", "10MB/s": "10MB"}
	for in, want := range cases {
		if got, err := parseSpeedInput(in); err != nil || got != want {
			t.Errorf("parseSpeedInput(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got := speedText(speedToBps("1MB")); got != "1 MB/s（实际网速 8 Mbps）" {
		t.Errorf("speedText 1MB = %q", got)
	}
	if got := speedText(speedToBps("100Mbps")); got != "12.5 MB/s（实际网速 100 Mbps）" {
		t.Errorf("speedText 100Mbps = %q", got)
	}
}

func TestFileContainsAcrossChunks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	data := make([]byte, 600*1024)
	copy(data[256*1024+10:], "bandwidth-limiter")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	if !fileContains(p, []byte("bandwidth-limiter")) {
		t.Fatal("跨块的字符串没找到")
	}
	if fileContains(p, []byte("nope-not-here")) {
		t.Fatal("误报")
	}
}
