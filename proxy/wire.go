package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"reflect"
	"slices"
	"sync"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Other protocol versions.
//
// The relay decodes and encodes packets with gophertunnel's own structs, which are those of
// protocol.CurrentProtocol (1.26.50). A peer of another version registered with RegisterProtocol
// (a minecraft.Protocol, as Dragonfly and gophertunnel's Listener use them) gets its packets
// translated at the ProxyConn: ReadPackets hands out every packet in the 1.26.50 format, and
// WritePackets turns them back into the peer's format. The rest of the relay never sees another
// version.
//
// Only packets whose format differs are translated (see newWire), and one that passes through the
// relay unchanged reaches the other peer as the exact bytes it came in as: the translated copy
// carries the original behind its length (tag/untag), so fields 1.26.50 has no place for are not
// lost between a client and a server of the same newer version. A packet the relay changed, or
// built itself, is translated from the 1.26.50 struct.

var (
	protocolsMu sync.RWMutex
	protocols   = map[int32]minecraft.Protocol{}
	wires       = map[int32]*Wire{}
)

// RegisterProtocol makes the relay accept clients of p's version and speak it to servers. The
// relay's own version, protocol.CurrentProtocol, is always supported.
func RegisterProtocol(p minecraft.Protocol) {
	if p.ID() == protocol.CurrentProtocol {
		return
	}
	protocolsMu.Lock()
	defer protocolsMu.Unlock()
	protocols[p.ID()] = p
	wires[p.ID()] = newWire(p)
}

// Supported reports whether the relay can serve a client of protocol id.
func Supported(id int32) bool {
	if id == protocol.CurrentProtocol {
		return true
	}
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	_, ok := protocols[id]
	return ok
}

// SupportedProtocols lists the protocol numbers the relay serves, lowest first.
func SupportedProtocols() []int32 {
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	out := []int32{protocol.CurrentProtocol}
	for id := range protocols {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// VersionName is the game version of a supported protocol ("1.26.60"), or the number itself.
func VersionName(id int32) string {
	if id == protocol.CurrentProtocol {
		return protocol.CurrentVersion
	}
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	if p, ok := protocols[id]; ok {
		return p.Ver()
	}
	return fmt.Sprintf("protocol %d", id)
}

// Protocol returns the registered minecraft.Protocol of protocol id, for gophertunnel dialers
// (nil for the relay's own version, which is gophertunnel's default).
func Protocol(id int32) (minecraft.Protocol, bool) {
	if id == protocol.CurrentProtocol {
		return nil, true
	}
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	p, ok := protocols[id]
	return p, ok
}

// wireFor is the translation for a peer of protocol id: nil for the relay's own version (and for
// an unknown one, which never gets past the login).
func wireFor(id int32) *Wire {
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	return wires[id]
}

// Packet directions: who sends the packet.
const (
	fromServer = 0
	fromClient = 1
)

// Wire translates one protocol version's packets to and from the relay's own.
type Wire struct {
	proto minecraft.Protocol
	id    int32
	// pools are the version's packets, latest the relay's own, per direction.
	pools, latest [2]packet.Pool
	// changed are the packet IDs to translate, per direction.
	changed [2]map[uint32]bool
	// clientOnly are the packets a client of the version sends that the relay's own version
	// doesn't have: a backend logged in at the relay's version can't read them (Dragonfly
	// disconnects the player), so they are dropped.
	clientOnly map[uint32]bool
}

func newWire(p minecraft.Protocol) *Wire {
	w := &Wire{proto: p, id: p.ID()}
	w.pools = [2]packet.Pool{fromServer: p.Packets(false), fromClient: p.Packets(true)}
	w.latest = [2]packet.Pool{fromServer: packet.NewServerPool(), fromClient: packet.NewClientPool()}
	// A version with its own reader or writer may encode any packet differently, unless it says
	// which packets its IO methods touch (customIO).
	customIO := reflect.TypeOf(p.NewReader(bytes.NewBuffer(nil), 0, false)) != reflect.TypeOf(protocol.NewReader(bytes.NewBuffer(nil), 0, false)) ||
		reflect.TypeOf(p.NewWriter(bytes.NewBuffer(nil), 0)) != reflect.TypeOf(protocol.NewWriter(bytes.NewBuffer(nil), 0))
	all := customIO
	var ioPackets []uint32
	if c, ok := p.(customIOProtocol); ok && customIO {
		all, ioPackets = false, c.CustomIOPackets()
	}
	w.clientOnly = map[uint32]bool{}
	for d := range w.pools {
		w.changed[d] = map[uint32]bool{}
		for id, f := range w.pools[d] {
			lf, ok := w.latest[d][id]
			if !ok {
				// New in that version: the relay can't read it. A server's is passed on as it is,
				// a client's dropped (clientOnly).
				if d == fromClient {
					w.clientOnly[id] = true
				}
				continue
			}
			// A version's own struct for a packet means a format of its own.
			if all || slices.Contains(ioPackets, id) || reflect.TypeOf(f()) != reflect.TypeOf(lf()) {
				w.changed[d][id] = true
			}
		}
		for id := range w.latest[d] {
			if _, ok := w.pools[d][id]; !ok {
				w.changed[d][id] = true // gone or renumbered: ConvertFromLatest knows
			}
		}
	}
	return w
}

// ID is the protocol number.
func (w *Wire) ID() int32 { return w.id }

// customIOProtocol is a Protocol with its own reader and writer that names the packets whose
// encoding those change. Without it every packet of the version is translated.
type customIOProtocol interface {
	CustomIOPackets() []uint32
}

// toLatest turns a packet read from the peer into the relay's format.
func (w *Wire) toLatest(raw []byte, dir int) [][]byte {
	hdr, payload, ok := header(raw)
	if ok && dir == fromClient && w.clientOnly[hdr.PacketID] {
		return nil
	}
	if !ok || !w.changed[dir][hdr.PacketID] {
		return [][]byte{raw}
	}
	f, ok := w.pools[dir][hdr.PacketID]
	if !ok {
		return [][]byte{raw}
	}
	pk := f()
	if err := unmarshal(pk, w.proto.NewReader(bytes.NewBuffer(payload), 0, false)); err != nil {
		return [][]byte{raw} // the relay won't make sense of it either: passed on as it came
	}
	conv, err := convert(w.proto.ConvertToLatest, pk)
	if err != nil {
		return [][]byte{raw}
	}
	out := make([][]byte, 0, len(conv))
	for _, c := range conv {
		out = append(out, encode(hdr, c, func(b *bytes.Buffer) protocol.IO { return protocol.NewWriter(b, 0) }))
	}
	if len(out) == 1 {
		out[0] = tag(out[0], raw, w.id)
	}
	return out
}

// fromLatest turns a packet in the relay's format into the peer's.
func (w *Wire) fromLatest(raw []byte, dir int) [][]byte {
	if orig, ok := untag(raw, w.id); ok {
		return [][]byte{orig}
	}
	hdr, payload, ok := header(raw)
	if !ok || !w.changed[dir][hdr.PacketID] {
		return [][]byte{raw}
	}
	f, ok := w.latest[dir][hdr.PacketID]
	if !ok {
		return [][]byte{raw}
	}
	pk := f()
	if err := unmarshal(pk, protocol.NewReader(bytes.NewBuffer(payload), 0, false)); err != nil {
		return [][]byte{raw}
	}
	conv, err := convert(w.proto.ConvertFromLatest, pk)
	if err != nil {
		return nil // never send the peer a packet in a format it doesn't read
	}
	out := make([][]byte, 0, len(conv))
	for _, c := range conv {
		out = append(out, encode(hdr, c, func(b *bytes.Buffer) protocol.IO { return w.proto.NewWriter(b, 0) }))
	}
	return out
}

func header(raw []byte) (*packet.Header, []byte, bool) {
	buf := bytes.NewBuffer(raw)
	hdr := &packet.Header{}
	if err := hdr.Read(buf); err != nil {
		return nil, nil, false
	}
	return hdr, buf.Bytes(), true
}

func unmarshal(pk packet.Packet, r protocol.IO) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("decode %T: %v", pk, v)
		}
	}()
	pk.Marshal(r)
	return nil
}

func convert(fn func(packet.Packet, *minecraft.Conn) []packet.Packet, pk packet.Packet) (out []packet.Packet, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("convert %T: %v", pk, v)
		}
	}()
	return fn(pk, nil), nil
}

// encode writes pk with the sub-client IDs of hdr.
func encode(hdr *packet.Header, pk packet.Packet, writer func(*bytes.Buffer) protocol.IO) []byte {
	b := bytes.NewBuffer(make([]byte, 0, 64))
	h := *hdr
	h.PacketID = pk.ID()
	_ = h.Write(b)
	pk.Marshal(writer(b))
	return b.Bytes()
}

// A translated packet keeps its original behind its length:
// magic | protocol | hash of the translation | length of the original | original.
const tagMagic = 0x657269775f6e6362 // "bcn_wire"

const tagHeader = 8 + 4 + 8 + 4

var tagSeed = maphash.MakeSeed()

func tag(native, orig []byte, proto int32) []byte {
	b := make([]byte, len(native), len(native)+tagHeader+len(orig))
	copy(b, native)
	t := b[len(native) : len(native)+tagHeader]
	binary.LittleEndian.PutUint64(t[0:], tagMagic)
	binary.LittleEndian.PutUint32(t[8:], uint32(proto))
	binary.LittleEndian.PutUint64(t[12:], maphash.Bytes(tagSeed, native))
	binary.LittleEndian.PutUint32(t[20:], uint32(len(orig)))
	return append(b[:len(native)+tagHeader], orig...)[:len(native)]
}

// untag returns the original of a translated packet the relay passed on unchanged, for a peer of
// the same protocol.
func untag(b []byte, proto int32) ([]byte, bool) {
	if cap(b)-len(b) < tagHeader {
		return nil, false
	}
	t := b[len(b):cap(b)]
	if binary.LittleEndian.Uint64(t[0:]) != tagMagic || int32(binary.LittleEndian.Uint32(t[8:])) != proto {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint32(t[20:]))
	if len(t) < tagHeader+n || binary.LittleEndian.Uint64(t[12:]) != maphash.Bytes(tagSeed, b) {
		return nil, false // changed by the relay since
	}
	return t[tagHeader : tagHeader+n : tagHeader+n], true
}
