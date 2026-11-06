package bridge

import (
	"os"
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func debugTestSession(name string) *switchSession {
	return &switchSession{id: login.IdentityData{DisplayName: name}}
}

func debugTestPayload(t *testing.T, pk packet.Packet) (uint32, []byte) {
	id, payload, err := splitPacket(encodePacket(pk))
	if err != nil {
		t.Fatal(err)
	}
	return id, payload
}

func TestDebugCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	if got := DebugCommand("all Nonsense"); !strings.Contains(got[0], "no packet called Nonsense") {
		t.Fatalf("%v", got)
	}
	if got := DebugCommand(`"Some Player" text`); !strings.Contains(got[0], "Logging Some Player") {
		t.Fatalf("%v", got)
	}
	d := debugActive.Load()
	if d == nil || !d.types["Text"] {
		t.Fatal("not started for Text")
	}
	id, payload := debugTestPayload(t, &packet.Text{TextType: packet.TextTypeChat, Message: "hello there"})
	mid, mpayload := debugTestPayload(t, &packet.MovePlayer{})
	s := debugTestSession("Some Player")
	s.debugPacket("client", id, payload)
	s.debugPacket("client", mid, mpayload)                       // not chosen
	debugTestSession("Other").debugPacket("client", id, payload) // not this player

	path := d.path
	if got := DebugCommand("off"); !strings.Contains(got[0], "1 packets") {
		t.Fatalf("%v", got)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "# player Some Player") {
		t.Fatalf("no player line:\n%s", text)
	}
	if !strings.Contains(text, "hello there") || strings.Contains(text, "MovePlayer") || strings.Contains(text, "Other") {
		t.Fatalf("log:\n%s", text)
	}
}

func TestDebugAllLeavesOutNoiseAndContents(t *testing.T) {
	t.Chdir(t.TempDir())
	DebugCommand("all")
	d := debugActive.Load()
	id, payload := debugTestPayload(t, &packet.Text{Message: "secret words"})
	mid, mpayload := debugTestPayload(t, &packet.MovePlayer{})
	s := debugTestSession("A")
	s.debugPacket("client", id, payload)
	s.debugPacket("client", mid, mpayload)
	path := d.path
	DebugCommand("off")
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "Text") || strings.Contains(text, "MovePlayer") || strings.Contains(text, "secret words") {
		t.Fatalf("log:\n%s", text)
	}
}
