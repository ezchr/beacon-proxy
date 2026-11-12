package bridge

import (
	"github.com/ezchr/beacon-proxy/internal/anticheat"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// acConn is a session as the anticheat sees it.
type acConn struct{ s *switchSession }

func (c acConn) SendClient(pk packet.Packet) { c.s.sendClient(pk) }
func (c acConn) Disconnect(msg string)       { c.s.disconnect(msg) }

var _ anticheat.Conn = acConn{}

// AlertStaff sends msg to every admin online on the network.
func (n *Network) AlertStaff(msg string) {
	n.sessions.Range(func(_, v any) bool {
		s := v.(*switchSession)
		if n.IsAdmin(s.id.XUID, s.id.DisplayName) {
			s.message("%s", msg)
		}
		return true
	})
}
