package bridge

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Endstone reads the PEM's body lines as base64 and wants a 120-byte P-384 public key.
func TestEndstoneKeyFileIsWhatEndstoneReads(t *testing.T) {
	dir := t.TempDir()
	keyFile, pubFile := filepath.Join(dir, "k.key"), filepath.Join(dir, "k.pem")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key, err := loadEndstoneKey(keyFile, pubFile, log)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(pubFile)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); !strings.HasPrefix(line, "-----") {
			body += line
		}
	}
	der, err := base64.StdEncoding.DecodeString(body)
	prefix := []byte("\x30\x76\x30\x10\x06\x07\x2A\x86\x48\xCE\x3D\x02\x01\x06\x05\x2B\x81\x04\x00\x22\x03\x62\x00\x04")
	if err != nil || len(der) != 120 || !bytes.HasPrefix(der, prefix) {
		t.Fatalf("not a P-384 public key as Endstone checks it: %d bytes, %v", len(der), err)
	}
	again, err := loadEndstoneKey(keyFile, pubFile, log)
	if err != nil || !again.Equal(key) {
		t.Fatalf("the key must be kept across starts: %v", err)
	}
}

func TestAddressIP(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.5:19132":    "203.0.113.5",
		"[2001:db8::1%eth0]:5": "2001:db8::1",
		"":                     "127.0.0.1",
		"198.51.100.7":         "198.51.100.7",
		"3759370044 (3236714304) (udp6 host [2001:db8::7]:49626 (resolved: [2001:db8::7]:49626))": "2001:db8::7",
		"1 (2) (udp4 srflx 203.0.113.9:5000 (resolved: 203.0.113.9:5000))":                        "203.0.113.9",
	} {
		if got := addressIP(in); got != want {
			t.Errorf("addressIP(%q) = %q, want %q", in, got, want)
		}
	}
}
