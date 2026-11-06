package bridge

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Packet logging, to find out what really goes between players and servers: the debug command of
// the console and of the control API (docs/debugging.md). It writes to a file of its own in
// debug/, stops by itself, and costs nothing while off. Logins never pass through here: the
// session starts after them, so tokens cannot end up in a log.

const (
	debugFor      = 5 * time.Minute
	debugMaxBytes = 50 << 20
	debugKeep     = 10
	debugDir      = "debug"
	// debugMaxText is how much of a packet's contents a line shows.
	debugMaxText = 2000
)

// debugNoisy are the packets that go by many times a second. They are left out unless named or
// asked for with full.
var debugNoisy = map[string]bool{
	"PlayerAuthInput": true, "MovePlayer": true, "MoveActorAbsolute": true, "MoveActorDelta": true,
	"SetActorMotion": true, "SetActorData": true, "LevelChunk": true, "SubChunk": true, "SubChunkRequest": true,
	"NetworkChunkPublisherUpdate": true, "UpdateBlock": true, "LevelSoundEvent": true, "LevelEvent": true,
	"Animate": true, "UpdateAttributes": true, "ClientCacheBlobStatus": true, "ClientCacheMissResponse": true,
	"NetworkStackLatency": true, "SetTime": true, "TickSync": true, "SpawnParticleEffect": true,
}

// debugPackets are the packets by ID, to name and read them.
var debugPackets = sync.OnceValue(func() map[uint32]func() packet.Packet {
	out := map[uint32]func() packet.Packet{}
	for id, f := range packet.NewServerPool() {
		out[id] = f
	}
	for id, f := range packet.NewClientPool() {
		out[id] = f
	}
	return out
})

// debugName is a packet's name, such as Text.
func debugName(id uint32) string {
	if f, ok := debugPackets()[id]; ok {
		return strings.TrimPrefix(fmt.Sprintf("%T", f()), "*packet.")
	}
	return "Unknown" + strconv.Itoa(int(id))
}

// debugTypes finds the packet names typed, in any case. It returns the first it doesn't know.
func debugTypes(typed []string) (map[string]bool, string) {
	known := map[string]string{}
	for id := range debugPackets() {
		n := debugName(id)
		known[strings.ToLower(n)] = n
	}
	out := map[string]bool{}
	for _, t := range typed {
		n, ok := known[strings.ToLower(t)]
		if !ok {
			return nil, t
		}
		out[n] = true
	}
	return out, ""
}

// debugRun is packet logging under way.
type debugRun struct {
	player   string // lower case; "" for everyone
	types    map[string]bool
	full     bool
	path     string
	started  time.Time
	mu       sync.Mutex
	file     *os.File
	w        *bufio.Writer
	written  int64
	lines    int64
	stopOnce sync.Once
	timer    *time.Timer
}

var debugActive atomic.Pointer[debugRun]

// debugPacket logs a packet if packet logging takes it. dir is who sent it.
func (s *switchSession) debugPacket(dir string, id uint32, payload []byte) {
	d := debugActive.Load()
	if d == nil || (d.player != "" && d.player != strings.ToLower(s.id.DisplayName)) {
		return
	}
	name := debugName(id)
	switch {
	case len(d.types) > 0:
		if !d.types[name] {
			return
		}
	case !d.full && debugNoisy[name]:
		return
	}
	line := fmt.Sprintf("%s  %-16s %-8s %-6s %-28s %6d B", time.Now().Format("15:04:05.000"), s.id.DisplayName, s.serverName(), dir, name, len(payload))
	// Contents for one player or chosen packets: everything from everyone would be too much.
	if d.player != "" || len(d.types) > 0 {
		line += "  " + debugContents(id, payload)
	}
	d.write(line)
}

// debugContents is a packet's fields as text.
func debugContents(id uint32, payload []byte) string {
	f, ok := debugPackets()[id]
	if !ok {
		return "(unknown packet)"
	}
	pk := f()
	if err := decodeInto(pk, payload); err != nil {
		return "(could not read it: " + err.Error() + ")"
	}
	text := strings.ReplaceAll(fmt.Sprintf("%+v", pk), "\n", " ")
	if len(text) > debugMaxText {
		text = strings.ToValidUTF8(text[:debugMaxText], "") + " ...cut"
	}
	return text
}

func (d *debugRun) write(line string) {
	d.mu.Lock()
	if d.w == nil {
		d.mu.Unlock()
		return
	}
	n, _ := d.w.WriteString(line + "\n")
	d.written += int64(n)
	d.lines++
	over := d.written >= debugMaxBytes
	d.mu.Unlock()
	if over {
		go d.stop("it reached the size limit")
	}
}

// stop ends the run, once.
func (d *debugRun) stop(why string) {
	d.stopOnce.Do(func() {
		debugActive.CompareAndSwap(d, nil)
		d.timer.Stop()
		d.mu.Lock()
		defer d.mu.Unlock()
		_, _ = fmt.Fprintf(d.w, "# stopped: %s, %d packets\n", why, d.lines)
		_ = d.w.Flush()
		_ = d.file.Close()
		d.w = nil
	})
}

// debugStart begins a run.
func debugStart(player string, types map[string]bool, full bool) (*debugRun, error) {
	if err := os.MkdirAll(debugDir, 0o755); err != nil {
		return nil, err
	}
	debugPrune()
	now := time.Now()
	path := filepath.Join(debugDir, "packets-"+now.Format("2006-01-02_15-04-05")+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	d := &debugRun{player: strings.ToLower(player), types: types, full: full, path: path, started: now, file: f, w: bufio.NewWriter(f)}
	_, _ = fmt.Fprintf(d.w, "# time  player  server  from  packet  size  contents\n")
	d.timer = time.AfterFunc(debugFor, func() { d.stop("the time limit passed") })
	if old := debugActive.Swap(d); old != nil {
		old.stop("a new one started")
	}
	return d, nil
}

// debugPrune leaves room for one more file among the newest debugKeep.
func debugPrune() {
	files, _ := filepath.Glob(filepath.Join(debugDir, "packets-*.log"))
	slices.Sort(files) // names sort by time
	for len(files) >= debugKeep {
		_ = os.Remove(files[0])
		files = files[1:]
	}
}

// debugHelp is what debug says with nothing to do.
func debugHelp() []string {
	return []string{
		"debug <player>            log what one player sends and gets, with contents",
		"debug <player> Text       only those packets (any number of names)",
		"debug all                 everyone: packet names and sizes",
		"debug all Text            everyone, those packets, with contents",
		"add full to include movement, chunks and other packets that go by all the time",
		"debug off                 stop and show the file",
		"It stops by itself after 5 minutes or 50 MB. Files are in " + debugDir + "/, the newest 10 are kept.",
	}
}

// DebugCommand runs the debug command, for the console and the control API. A name with spaces goes
// in quotes.
func DebugCommand(line string) []string {
	args := splitCommand(line)
	if len(args) == 0 {
		out := []string{"Packet logging is off."}
		if d := debugActive.Load(); d != nil {
			d.mu.Lock()
			lines := d.lines
			d.mu.Unlock()
			out = []string{fmt.Sprintf("Logging to %s: %d packets so far, %s left.", d.path, lines, (debugFor - time.Since(d.started)).Round(time.Second))}
		}
		return append(out, debugHelp()...)
	}
	if strings.EqualFold(args[0], "off") {
		d := debugActive.Load()
		if d == nil {
			return []string{"Packet logging is off."}
		}
		d.stop("debug off")
		return []string{fmt.Sprintf("Stopped. %d packets are in %s.", d.lines, d.path)}
	}
	player, rest := args[0], args[1:]
	if strings.EqualFold(player, "all") {
		player = ""
	}
	full := false
	rest = slices.DeleteFunc(slices.Clone(rest), func(s string) bool {
		if strings.EqualFold(s, "full") {
			full = true
			return true
		}
		return false
	})
	types, bad := debugTypes(rest)
	if bad != "" {
		return []string{fmt.Sprintf("There is no packet called %s. Names are as in Text, MobEquipment or InventoryTransaction.", bad)}
	}
	d, err := debugStart(player, types, full)
	if err != nil {
		return []string{"Could not start: " + err.Error()}
	}
	who := "everyone"
	if player != "" {
		who = player
	}
	return []string{fmt.Sprintf("Logging %s to %s for 5 minutes. Stop with: debug off", who, d.path)}
}
