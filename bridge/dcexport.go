package bridge

import "net"

// DatacenterMessage is shown to a player turned away for joining from a datacenter network.
const DatacenterMessage = dcBlockMessage

// DatacenterVerdict says whether a join from addr is turned away for coming from a datacenter
// network, and why, for the Java proxy (java.go): the same lists and rules as Bedrock joins.
func DatacenterVerdict(addr net.Addr) (blocked bool, verdict string, ip net.IP) {
	blocked, verdict, ip, _ = dcVerdict(addr)
	return blocked, verdict, ip
}

// LogJoin appends one join attempt to the shared joins.log.
func LogJoin(line string) { logJoin(line) }
