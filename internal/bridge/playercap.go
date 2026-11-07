package bridge

import "sync/atomic"

// playerCap is the most Bedrock players Beacon lets on at once (max-players); 0 means no cap.
// Admins always get in.
var playerCap atomic.Int64

// SetPlayerCap sets the cap; 0 or less turns it off.
func SetPlayerCap(n int) { playerCap.Store(int64(max(n, 0))) }

const fullMessage = "The server is full. Try again in a bit."

// takeSlot counts a joining player in and reports whether there was room. On false the player was
// not counted. Counting first and checking after keeps two joins at once from both taking the last slot.
func takeSlot(admin bool) bool {
	noteBedrock(1)
	if c := playerCap.Load(); c > 0 && !admin && onlineBedrock.Load() > c {
		noteBedrock(-1)
		return false
	}
	return true
}
