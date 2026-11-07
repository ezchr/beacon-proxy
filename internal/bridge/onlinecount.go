package bridge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The player count the server lists show is the whole network's: every Bedrock player on the relay
// (whichever server they are on) plus every Java player on Velocity. Each side writes its own number
// to a file in PlayerCountDir when someone joins or leaves (bedrock.txt here, java.txt by Velocity's
// DcBlock plugin), and reads the other's when a list asks.

// PlayerCountDir is where the counts are written: BEACON_PLAYERCOUNT_DIR, or "playercount".
var PlayerCountDir = func() string {
	if d := env("PLAYERCOUNT_DIR"); d != "" {
		return d
	}
	return "playercount"
}()

var (
	onlineBedrock atomic.Int64
	countFileMu   sync.Mutex
)

// noteBedrock counts a Bedrock player in (+1) or out (-1) and writes the new number.
func noteBedrock(delta int64) {
	n := onlineBedrock.Add(delta)
	countFileMu.Lock()
	defer countFileMu.Unlock()
	_ = os.MkdirAll(PlayerCountDir, 0o755)
	tmp := filepath.Join(PlayerCountDir, "bedrock.txt.tmp")
	if os.WriteFile(tmp, []byte(strconv.FormatInt(max(n, 0), 10)), 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(PlayerCountDir, "bedrock.txt"))
	}
}

// javaPlayers reads Velocity's count; 0 if it is missing or more than a day old (Velocity down).
func javaPlayers() int {
	p := filepath.Join(PlayerCountDir, "java.txt")
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) > 24*time.Hour {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return max(n, 0)
}

// NetworkPlayers is everyone online on the network: the relay's Bedrock players and Velocity's Java ones.
func NetworkPlayers() int {
	return int(max(onlineBedrock.Load(), 0)) + javaPlayers()
}

// javaCountKept reports whether the Java proxy keeps its count in PlayerCountDir (java.txt written
// within the last day).
func javaCountKept() bool {
	st, err := os.Stat(filepath.Join(PlayerCountDir, "java.txt"))
	return err == nil && time.Since(st.ModTime()) <= 24*time.Hour
}
