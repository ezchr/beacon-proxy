package bridge

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Packet logging, to find out what really goes between players and servers: the debug command of
// the console and of the control API (docs/debugging.md). It writes to a file of its own in
// debug/, stops by itself, and costs nothing while off. A player's login is logged as what it
// said, not as the packet, which holds their Xbox tokens.

const (
	debugFor      = 5 * time.Minute
	debugMaxBytes = 50 << 20
	debugKeep     = 10
	debugDir      = "debug"
	// debugMaxText is how much of a packet's contents a line shows.
	debugMaxText = 2000
)

// debugNoisy are the packets that go by many times a second, which quiet leaves out.
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

// debugFilter says which packets a run logs.
type debugFilter struct {
	player string          // lower case; "" for everyone
	types  map[string]bool // only these
	skip   map[string]bool // not these
	quiet  bool            // not the ones in debugNoisy
	from   string          // "client" or "server": only what they send
	server string          // only on this server
	has    string          // lower case: only packets with this in them
}

// debugParse reads the words after the player. It returns what it can't read.
func debugParse(words []string, f *debugFilter) string {
	var types, skip []string
	for _, w := range words {
		l := strings.ToLower(w)
		switch {
		case l == "quiet":
			f.quiet = true
		case l == "client" || l == "server":
			f.from = l
		case strings.HasPrefix(l, "on:") && len(l) > 3:
			f.server = w[3:]
		case strings.HasPrefix(l, "has:") && len(l) > 4:
			f.has = l[4:]
		case strings.HasPrefix(w, "-") && len(w) > 1:
			skip = append(skip, w[1:])
		default:
			types = append(types, w)
		}
	}
	var bad string
	if f.types, bad = debugTypes(types); bad != "" {
		return bad
	}
	f.skip, bad = debugTypes(skip)
	return bad
}

// debugRun is packet logging under way.
type debugRun struct {
	debugFilter
	path     string
	started  time.Time
	mu       sync.Mutex
	file     *os.File
	w        *bufio.Writer
	written  int64
	lines    int64
	seen     sync.Map // players with their line written
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
	case len(d.types) > 0 && !d.types[name],
		d.skip[name],
		d.quiet && len(d.types) == 0 && debugNoisy[name],
		d.from != "" && d.from != dir:
		return
	}
	server := s.serverName()
	if d.server != "" && !strings.EqualFold(d.server, server) {
		return
	}
	// Contents for one player or chosen packets: everything from everyone would be too much.
	contents := ""
	if d.player != "" || len(d.types) > 0 || d.has != "" {
		contents = debugContents(id, payload)
	}
	if d.has != "" && !strings.Contains(strings.ToLower(contents), d.has) {
		return
	}
	if _, seen := d.seen.LoadOrStore(s.id.XUID+s.id.DisplayName, true); !seen {
		d.write(s.debugWho(), false)
		d.write(s.debugLogin(), false)
		d.write(s.debugSkin(d), false)
	}
	line := fmt.Sprintf("%s  %-16s %-8s %-6s %-28s %6d B", time.Now().Format("15:04:05.000"), s.id.DisplayName, server, dir, name, len(payload))
	if contents != "" {
		line += "  " + contents
	}
	d.write(line, true)
}

// debugWho is the line that says who a player is, the first time they show up in a log.
func (s *switchSession) debugWho() string {
	ip := "unknown"
	if s.conn != nil {
		if a := s.remoteIP(); a != "" {
			ip = a
		}
	}
	return fmt.Sprintf("# player %s xuid=%s ip=%s device=%v model=%q version=%s", s.id.DisplayName, s.id.XUID, ip, s.cd.DeviceOS, s.cd.DeviceModel, s.cd.GameVersion)
}

// debugLogin is what the player's login said, read from it: the login packet itself holds their
// Xbox tokens, which stay out of logs. Long values, the skin's images and models, are only measured.
func (s *switchSession) debugLogin() string {
	data, err := json.Marshal(s.cd)
	if err != nil {
		return "# login: " + err.Error()
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return "# login: " + err.Error()
	}
	for k, v := range fields {
		switch t := v.(type) {
		case string:
			if len(t) > 200 {
				fields[k] = fmt.Sprintf("(%d characters)", len(t))
			}
		case []any:
			fields[k] = fmt.Sprintf("(%d entries)", len(t))
		}
	}
	id, _ := json.Marshal(s.id)
	out, _ := json.Marshal(fields)
	return "# identity " + string(id) + "\n# login " + string(out)
}

// debugSkin saves the player's skin as a picture beside the log and says where.
func (s *switchSession) debugSkin(d *debugRun) string {
	w, h := s.cd.SkinImageWidth, s.cd.SkinImageHeight
	pix, err := base64.StdEncoding.DecodeString(s.cd.SkinData)
	if err != nil || w <= 0 || h <= 0 || len(pix) != w*h*4 {
		return fmt.Sprintf("# skin %q: no picture (%dx%d, %d bytes)", s.cd.SkinID, w, h, len(pix))
	}
	name := strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, s.id.DisplayName)
	path := strings.TrimSuffix(d.path, ".log") + "-" + name + ".png"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "# skin: " + err.Error()
	}
	defer f.Close()
	img := &image.NRGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	if err := png.Encode(f, img); err != nil {
		return "# skin: " + err.Error()
	}
	return fmt.Sprintf("# skin %q %dx%d persona=%v saved as %s", s.cd.SkinID, w, h, s.cd.PersonaSkin, path)
}

// debugSecret are the packets whose contents are never logged: login tokens, the handshake that
// sets up encryption, and purchase receipts. A log is kept and passed around, so nothing in it may
// be of use to anyone who gets hold of it.
var debugSecret = map[string]bool{
	"Login": true, "SubClientLogin": true, "ServerToClientHandshake": true, "ClientToServerHandshake": true,
	"PurchaseReceipt": true,
}

// debugSecretCommands take a password, as login plugins do.
var debugSecretCommands = map[string]bool{
	"login": true, "l": true, "log": true, "register": true, "reg": true, "changepassword": true,
	"changepass": true, "cp": true, "password": true, "pass": true, "passwd": true, "auth": true,
}

// debugRedact takes what is secret out of a packet that is otherwise fine to log.
func debugRedact(pk packet.Packet) {
	switch p := pk.(type) {
	case *packet.ResourcePacksInfo:
		for i := range p.TexturePacks {
			if p.TexturePacks[i].ContentKey != "" {
				p.TexturePacks[i].ContentKey = "(hidden)" // what decrypts the pack
			}
		}
	case *packet.CommandRequest:
		p.CommandLine = debugCommandLine(p.CommandLine)
	case *packet.ModalFormResponse:
		if data, ok := p.ResponseData.Value(); ok {
			p.ResponseData = protocol.Option(debugFormResponse(data))
		}
	}
}

// debugCommandLine hides what follows a command that takes a password.
func debugCommandLine(line string) string {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "/"))
	if len(f) > 1 && debugSecretCommands[strings.ToLower(f[0])] {
		return "/" + f[0] + " (hidden)"
	}
	return line
}

// debugFormResponse hides what a player typed into a form, which can be anything, and keeps which
// buttons and options they chose.
func debugFormResponse(data []byte) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return []byte(`"(hidden)"`)
	}
	var hide func(v any) any
	hide = func(v any) any {
		switch t := v.(type) {
		case string:
			return "(typed text)"
		case []any:
			for i := range t {
				t[i] = hide(t[i])
			}
		}
		return v
	}
	out, _ := json.Marshal(hide(v))
	return out
}

// debugContents is a packet's fields as text.
func debugContents(id uint32, payload []byte) string {
	f, ok := debugPackets()[id]
	if !ok {
		return "(unknown packet)"
	}
	pk := f()
	if debugSecret[debugName(id)] {
		return "(hidden: it holds secrets)"
	}
	if err := decodeInto(pk, payload); err != nil {
		return "(could not read it)"
	}
	debugRedact(pk)
	text := strings.ReplaceAll(fmt.Sprintf("%+v", pk), "\n", " ")
	if len(text) > debugMaxText {
		text = strings.ToValidUTF8(text[:debugMaxText], "") + " ...cut"
	}
	return text
}

// write adds a line; count says whether it is a packet.
func (d *debugRun) write(line string, count bool) {
	d.mu.Lock()
	if d.w == nil {
		d.mu.Unlock()
		return
	}
	n, _ := d.w.WriteString(line + "\n")
	d.written += int64(n)
	if count {
		d.lines++
	}
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
func debugStart(filter debugFilter) (*debugRun, error) {
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
	d := &debugRun{debugFilter: filter, path: path, started: now, file: f, w: bufio.NewWriter(f)}
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
		skins, _ := filepath.Glob(strings.TrimSuffix(files[0], ".log") + "-*.png")
		for _, skin := range skins {
			_ = os.Remove(skin)
		}
		files = files[1:]
	}
}

// debugHelp is what debug says with nothing to do.
func debugHelp() []string {
	return []string{
		"debug <player>            log what one player sends and gets, with contents",
		"debug <player> Text       only those packets (any number of names)",
		"debug all                 every packet of everyone: names and sizes",
		"debug all Text            everyone, those packets, with contents",
		"Filters, in any mix after the player:",
		"  Text MobEquipment       only these packets",
		"  -MovePlayer             not this packet",
		"  quiet                   not movement, chunks and what else goes by all the time",
		"  client / server         only what that side sends",
		"  on:survival             only on this server",
		"  has:diamond             only packets with this word in them",
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
	player := args[0]
	if strings.EqualFold(player, "all") {
		player = ""
	}
	filter := debugFilter{player: strings.ToLower(player)}
	if bad := debugParse(args[1:], &filter); bad != "" {
		return []string{fmt.Sprintf("There is no packet called %s. Names are as in Text, MobEquipment or InventoryTransaction.", bad)}
	}
	d, err := debugStart(filter)
	if err != nil {
		return []string{"Could not start: " + err.Error()}
	}
	who := "everyone"
	if player != "" {
		who = player
	}
	return []string{fmt.Sprintf("Logging %s to %s for 5 minutes. Stop with: debug off", who, d.path)}
}
