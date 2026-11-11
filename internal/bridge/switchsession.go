package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/ezchr/beacon-proxy/internal/proxy"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
)

// switchSession is one player of a Network: their connection to the relay, which outlives every
// backend they are moved between.
type switchSession struct {
	refusedWhy string // why a moderator's last command was refused, for the staff log
	n          *Network
	cfg        Config
	log        *slog.Logger
	client     *proxy.ProxyConn
	conn       net.Conn
	id         login.IdentityData
	cd         login.ClientData
	proto      int32
	pool       packet.Pool

	ctx    context.Context
	cancel context.CancelCauseFunc
	// packGen is Network.packGen when this client logged in.
	packGen uint64
	// reply takes messages in place of a client: the console's stand-in session (moderation.go).
	reply func(string)
	// limited marks a stand-in session (reply) as a moderator's (ModerateAs).
	limited bool
	// keepTags keeps the colour tags in what a stand-in session says (the Java proxy shows them).
	keepTags bool
	// rejoining is set once the player is being sent to join again (record).
	rejoining atomic.Bool

	wmu sync.Mutex // client writes

	mu sync.Mutex
	// What the client was given at its join, fixed for the whole connection.
	clientRID uint64
	clientUID int64
	palette   *blockPalette
	itemIDs   map[string]int32 // the client's item numbering (the union's)
	// The backend the player is on and the one they are being moved to.
	cur      *link
	transfer *transfer
	switchMu sync.Mutex // one move at a time

	// Client state the relay has to undo on a move (as WaterdogPE's EntityTracker).
	dimension    int32
	settle       chunkSettle       // chunks around the spawn, sent again once the client settled (dimreplay.go)
	placeholders map[[2]int32]bool // columns the relay's own request-mode chunks still fill (placeholders.go)
	// settling, settlePos, settleUntil: see settle.go.
	settling     atomic.Bool
	settlePos    mgl32.Vec3
	settleUntil  time.Time
	settleLogged int
	// awaitGround is closed when the server's sub-chunks for awaitCol, the spawn column of a move,
	// pass (transfer.phase2Done). Nil when nothing waits.
	awaitGround chan struct{}
	awaitCol    [2]int32
	// columns are the columns the player's server sent whole, subPending the sub-chunk requests
	// waiting for one, subServed how many requests were answered from them (subserve.go).
	columns     map[[2]int32]*servedColumn
	subPending  map[[2]int32][]pendingSub
	subServed   int
	subw        subWatch
	chunkRadius int32
	maxRadius   uint8
	// unchecked: left out of the build's player checks (checks.go). radiusSeen: the client has asked
	// for a view distance before.
	unchecked bool
	// packets counts the client's packets for the packet rate limit (limits.go); readClient only.
	packets    packetMeter
	radiusSeen bool
	// scrub: not an admin of the relay, so command block and NPC commands are blanked on the way
	// to the client (opfilter.go).
	scrub bool
	// scrubbed counts the packets with something to blank, by packet name.
	scrubbed     map[string]int
	subChunkMode bool
	entities     map[int64]bool
	players      map[uuid.UUID]bool
	bossBars     map[int64]bool
	waypoints    map[uuid.UUID]bool // locator bar waypoints the current server added
	objectives   map[string]bool
	scores       map[int64]protocol.ScoreboardEntry
	links        map[int64]int64
	containers   map[byte]byte
	fog          bool
	inputLocks   uint32
	hiddenHud    map[int32]bool
	dead         bool
	proxyRespawn bool

	// hold is set while the player waits on Beacon for their server to come back (hold.go);
	// lastHold is the last hold that ended with them back on it.
	hold     *holdState
	lastHold endedHold

	// Split-screen (subclient_guest.go). guests are a host's split-screen players by sub-client id;
	// a guest has its host, its sub-client id, inbox (its packets from the host's reader), guestLogin
	// (a Login made from its SubClientLogin) and derivedID (it plays as an identity made from the host's).
	guests map[byte]*switchSession
	// In passthrough mode (subclient_passthrough.go): ptGuests is the link each guest logged in on,
	// ptItems the guests that got their item list, ptUnion the union the client joined with.
	ptGuests map[byte]*link
	ptItems  map[byte]bool
	ptIDs    map[byte]login.IdentityData
	ptLogins map[byte][]byte // each passed-through guest's SubClientLogin, to log in again after a move
	// subRequest is a guest's login request (its SubClientLogin), startServer the server it joins.
	subRequest  []byte
	startServer string
	ptUnion     *union
	host        *switchSession
	sub         byte
	derivedID   bool
	inbox       chan []byte
	guestLogin  []byte
}

// link is the relay's connection to one backend for one player.
type link struct {
	server    ServerConfig
	c         *proxy.ProxyConn
	conn      backendConn
	startGame *packet.StartGame
	// pre is what the server sent between the login and StartGame other than the handshake
	// itself (Dragonfly sends VoxelShapes and JigsawStructureData there). A client joining needs
	// them: without the structure data it fails with "missing structure data" (error 148).
	pre [][]byte
	// itemReg is the server's item list when it came before StartGame (Geyser sends it there,
	// Dragonfly after): the item renumbering is built from it once the rewriter exists.
	itemReg   *packet.ItemRegistry
	started   chan struct{} // closed once StartGame arrived
	spawned   bool
	rw        *rewriter
	closed    chan struct{}
	closeOnce sync.Once
	err       error // why the backend ended the link
}

func (l *link) close() {
	l.closeOnce.Do(func() {
		close(l.closed)
		_ = l.conn.Close()
	})
}

// HandleNetworkConn is HandleConn for a relay with a Network: the player can be moved between the
// network's servers without leaving.
func HandleNetworkConn(ctx context.Context, conn *nethernet.Conn, cfg Config) {
	handleNetwork(ctx, conn, true, cfg)
}

func handleNetwork(ctx context.Context, conn backendConn, isNetherNet bool, cfg Config) {
	n := cfg.Network
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	defer conn.Close()
	client := proxy.NewProxyConn(conn, isNetherNet)
	client.SetAuthEnabled(true)
	client.SetClientEncryption(!cfg.DisableClientEncryption)
	if err := client.ReadLoop(); err != nil {
		log.Warn("client failed to log in", "err", err)
		_ = client.WritePacket(&packet.Disconnect{Message: "Error: " + err.Error()})
		time.Sleep(time.Second)
		return
	}
	id := client.IdentityData()
	if id.XUID == "" {
		log.Warn("client finished login without an authenticated identity - dropping it")
		return
	}
	if cfg.AllowXUID != nil && !cfg.AllowXUID(id.XUID) {
		log.Warn("rejected connection from disallowed xuid", "xuid", id.XUID, "displayName", id.DisplayName)
		_ = client.WritePacket(&packet.Disconnect{Message: "You are not allowed to join this server."})
		time.Sleep(time.Second)
		return
	}
	log = log.With("player", id.DisplayName)
	log.Info("player authenticated via xbox live", "xuid", id.XUID)
	if msg, closed := Closed(); closed && !n.IsAdmin(id.XUID, id.DisplayName) {
		log.Info("join refused: the network is closed")
		_ = client.WritePacket(&packet.Disconnect{Message: msg})
		time.Sleep(time.Second)
		return
	}
	{
		ip := ""
		if _, _, addr, _ := dcVerdict(conn.RemoteAddr()); addr != nil {
			ip = addr.String()
		}
		if msg, banned := n.refuseLogin(id.XUID, id.DisplayName, ip); banned {
			log.Info("join refused: banned", "xuid", id.XUID)
			_ = client.WritePacket(&packet.Disconnect{Message: msg})
			time.Sleep(time.Second)
			return
		}
	}
	if blocked, verdict, ip, kind := dcVerdict(conn.RemoteAddr()); true {
		cd := client.ClientData()
		logJoin(fmt.Sprintf("%s bedrock name=%s xuid=%s ip=%s route=%s device=%d version=%s title=%s %s",
			time.Now().UTC().Format(time.RFC3339), id.DisplayName, id.XUID, ip, kind, int(cd.DeviceOS), cd.GameVersion, id.TitleID, verdict))
		if blocked {
			log.Warn("datacenter join turned away", "ip", ip, "verdict", verdict)
			_ = client.WritePacket(&packet.Disconnect{Message: dcBlockMessage})
			time.Sleep(time.Second)
			return
		}
	}
	if _, _, ip, _ := dcVerdict(conn.RemoteAddr()); ip != nil && loginTooSoon(ip.String()) {
		log.Info("join refused: login rate limit", "ip", ip)
		_ = client.WritePacket(&packet.Disconnect{Message: loginTooFastMessage})
		time.Sleep(time.Second)
		return
	}
	if _, _, ip, _ := dcVerdict(conn.RemoteAddr()); ip != nil {
		release, ok := holdSession(ip.String(), id.XUID)
		if !ok {
			log.Info("join refused: sessions per IP", "ip", ip)
			_ = client.WritePacket(&packet.Disconnect{Message: tooManySessionsMessage})
			time.Sleep(time.Second)
			return
		}
		defer release()
	}
	// The network's player count (onlinecount.go), and the cap (max-players, playercap.go).
	if !takeSlot(n.IsAdmin(id.XUID, id.DisplayName)) {
		log.Info("join refused: full", "cap", playerCap.Load())
		_ = client.WritePacket(&packet.Disconnect{Message: fullMessage})
		time.Sleep(time.Second)
		return
	}
	defer noteBedrock(-1)
	if cfg.WorldPlayers != nil {
		cfg.WorldPlayers.Add(1)
		defer cfg.WorldPlayers.Add(-1)
	}
	// The build's own player checks, if it has any (checks.go).
	unchecked := true
	if Checks != nil {
		var kick string
		unchecked, kick = Checks.Login(id, client.ClientData(), conn.RemoteAddr(), log)
		if kick != "" {
			_ = client.WritePacket(&packet.Disconnect{Message: kick})
			time.Sleep(time.Second)
			return
		}
	}
	// The servers only get the relay's re-signed login, which GeyserMC's skin API refuses.
	uploadSkin(client.RawLoginPacket(), log)
	if lr, ok := conn.(latencyReporter); ok {
		entry := registerPing(id.XUID, lr)
		defer unregisterPing(id.XUID, entry)
	}

	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cd := client.ClientData()
	cd.ThirdPartyName = id.DisplayName
	if cd.PlayFabID == "" {
		// 1.26.60 clients no longer put their PlayFab ID in the client data, only in the signed token, and
		// the servers do not get the token: without it other 1.26.60 clients cannot build a persona skin.
		cd.PlayFabID = strings.ToLower(id.PlayFabID)
	}
	s := &switchSession{
		packGen: n.packGen.Load(),
		n:       n, cfg: cfg, log: log, client: client, conn: conn, id: id, cd: cd, proto: client.Protocol(),
		pool: client.Pool(), ctx: sctx, cancel: cancel,
		chunkRadius: 16, maxRadius: 16, unchecked: unchecked,
		entities: map[int64]bool{}, players: map[uuid.UUID]bool{}, bossBars: map[int64]bool{}, waypoints: map[uuid.UUID]bool{},
		objectives: map[string]bool{}, scores: map[int64]protocol.ScoreboardEntry{}, links: map[int64]int64{},
		containers: map[byte]byte{}, hiddenHud: map[int32]bool{},
	}
	s.scrub = !s.isAdmin()
	if old, ok := n.sessions.Swap(id.XUID, s); ok {
		old.(*switchSession).disconnect("You joined again from somewhere else.")
	}
	defer n.sessions.CompareAndDelete(id.XUID, s)

	if err := s.join(); err != nil {
		log.Warn("network: join failed", "err", err)
		s.disconnect("Could not join: " + err.Error())
		time.Sleep(time.Second)
		return
	}
	if cfg.OnJoin != nil {
		cfg.OnJoin(id.XUID, id.DisplayName)
	}
	go s.readClient()
	<-sctx.Done()
	if cause := context.Cause(sctx); cause != nil && !errors.Is(cause, context.Canceled) {
		log.Info("relay ended", "reason", cause)
	}
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if cur != nil {
		cur.close()
	}
	s.ptForgetAll()
}

func (s *switchSession) serverName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ""
	}
	return s.cur.server.Name
}

func (s *switchSession) writeClient(pks ...[]byte) error {
	if s.host != nil { // a split-screen player: the host's connection, addressed to their sub-client
		out := make([][]byte, len(pks))
		for i, raw := range pks {
			out[i] = subClientHeader(raw, s.sub)
		}
		return s.host.writeClient(out...)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.client.WritePackets(pks)
}

func (s *switchSession) sendClient(pks ...packet.Packet) {
	raws := make([][]byte, 0, len(pks))
	for _, pk := range pks {
		raws = append(raws, encodePacket(pk))
	}
	if err := s.writeClient(raws...); err != nil {
		s.cancel(fmt.Errorf("write to client: %w", err))
	}
}

// record keeps what a server sent at a join or move (Network.record). Resource packs newer than
// the ones this client logged in with mean it has the old ones (packs only download at a join),
// however the relay learned of them: it is sent to join again, once, to download them. Other
// changes keep it relayed (items are renumbered per server, and new entities come with new packs).
func (s *switchSession) record(server string, id uint32, payload []byte) {
	s.n.record(server, id, payload)
	if id != packet.IDResourcePacksInfo && id != packet.IDResourcePackStack {
		return
	}
	if s.n.packGen.Load() == s.packGen || !s.rejoining.CompareAndSwap(false, true) {
		return
	}
	s.log.Info("network: server has new resource packs, the player joins again to download them", "server", server)
	s.n.rememberRejoin(s.id.XUID, server)
	go s.rejoin()
}

// rejoin transfers the client to the network again (Network.RejoinAddress), or asks the player to
// join again when there is no address or the client stays.
func (s *switchSession) rejoin() {
	defer s.recoverSession("rejoin")
	select {
	case <-time.After(3 * time.Second): // a transfer while the client still loads is lost
	case <-s.ctx.Done():
		return
	}
	const msg = "The server just updated.\nJoin again to play."
	host, port, err := net.SplitHostPort(s.n.RejoinAddress)
	p, perr := strconv.ParseUint(port, 10, 16)
	if err != nil || perr != nil {
		s.disconnect(msg)
		return
	}
	s.sendClient(&packet.Transfer{Address: host, Port: uint16(p)})
	select {
	case <-time.After(5 * time.Second):
		s.disconnect(msg)
	case <-s.ctx.Done():
	}
}

func (s *switchSession) disconnect(msg string) {
	_ = s.writeClient(encodePacket(&packet.Disconnect{Message: msg}))
	s.cancel(errors.New("disconnected: " + msg))
}

// message sends the player a system chat line.
func (s *switchSession) message(format string, a ...any) {
	if s.reply != nil {
		if s.keepTags {
			s.reply(fmt.Sprintf(format, a...))
			return
		}
		s.reply(modTags.ReplaceAllString(fmt.Sprintf(format, a...), ""))
		return
	}
	s.sendClient(&packet.Text{TextType: packet.TextTypeRaw, Message: text.Colourf(format, a...)})
}

// join does the client's pack screen with the union of the network's packs, logs the player into the
// default server and hands its StartGame to the client with the union's blocks, items and entities.
func (s *switchSession) join() error {
	u, err := s.n.buildUnion()
	if err != nil {
		return err
	}
	if len(u.missing) > 0 {
		s.log.Warn("network: some servers were never joined, their content is missing for this player", "servers", u.missing)
	}
	s.palette = u.palette
	s.ptUnion = u

	first := s.cfg.FirstServer
	if s.startServer != "" {
		first = s.startServer // a split-screen player joins its host's server
	}
	if back := s.n.takeRejoin(s.id.XUID); back != "" {
		first = back // joining again for new packs: back to the server they were moving to
	}
	order := s.n.withFirstServer(s.n.joinOrder(s.proto, s.cd.ServerAddress), first, s.proto)
	s.log.Info("network: join order", "first", first, "order", serverNames(order))
	if len(order) == 0 {
		return fmt.Errorf("no server here supports %s yet", proxy.VersionName(s.proto))
	}
	// The player joins the first server in order that answers with a world; the others are tried in
	// turn while the client loads the resource packs.
	type dialed struct {
		l      *link
		server ServerConfig
		err    error
	}
	dialCh := make(chan dialed, 1)
	go func() {
		defer s.recoverSession("join dial")
		var last error
		for _, server := range order {
			l, err := s.openLink(server)
			if err == nil {
				select {
				case <-l.started:
					dialCh <- dialed{l: l, server: server}
					return
				case <-l.closed:
					err = l.err
				case <-time.After(backendDialTimeout):
					l.close()
					err = errors.New("no StartGame")
				}
			}
			last = fmt.Errorf("%s: %w", server.Name, err)
			s.log.Info("network: could not join a server, trying the next", "server", server.Name, "err", err)
		}
		dialCh <- dialed{err: last}
	}()

	if s.host != nil {
		// A split-screen player: the console has the packs already, and no handshake (as BDS).
		s.sendClient(&packet.PlayStatus{Status: packet.PlayStatusLoginSuccess})
	} else if err := s.packScreen(u); err != nil {
		return fmt.Errorf("resource packs: %w", err)
	}
	d := <-dialCh
	if d.err != nil {
		return d.err
	}
	l, server := d.l, d.server

	sg := *l.startGame
	_, bm, err := s.n.serverPalette(sg.Blocks, s.palette, sg.UseBlockNetworkIDHashes)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.clientRID, s.clientUID = sg.EntityRuntimeID, sg.EntityUniqueID
	s.dimension = sg.Dimension
	l.startGame.Blocks = nil // the palette has them; moves only compare StartGame's settings
	l.rw = newRewriter(sg.EntityRuntimeID, sg.EntityUniqueID, s.clientRID, s.clientUID, bm)
	s.itemIDs = map[string]int32{}
	for _, it := range u.items.Items {
		s.itemIDs[it.Name] = int32(it.RuntimeID)
	}
	s.setItems(l)
	s.cur = l
	s.mu.Unlock()

	sg.Blocks = u.blocks
	// The client always numbers blocks by the union's palette: a server numbering by network hashes
	// (block network id hashes on, as a world a Bedrock client hosts) is renumbered like any other.
	sg.UseBlockNetworkIDHashes = false
	sg.Experiments = u.exps
	sg.PlayerMovementSettings.RewindHistorySize = u.rewind
	sg.ExperimentsPreviouslyToggled = len(u.exps) > 0
	if MultiPlayerOff {
		sg.MultiPlayerGame = false
	}
	if len(l.pre) > 0 {
		if err := s.writeClient(l.pre...); err != nil {
			return err
		}
	}
	s.sendClient(&sg, u.items, u.actors)
	s.log.Info("network: joined", "server", server.Name, "blocksRenumbered", bm != nil)
	s.setBackendPing(l) // the server leg counts in /ping only for a server on another machine
	go s.readBackend(l, true)
	return nil
}

// packScreen gives the client the union's resource packs: the relay serves them itself.
func (s *switchSession) packScreen(u *union) error {
	s.sendClient(&packet.PlayStatus{Status: packet.PlayStatusLoginSuccess}, u.packsInfo)
	_ = s.conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	defer s.conn.SetReadDeadline(time.Time{})
	for {
		pks, err := s.client.ReadPackets()
		if err != nil {
			return err
		}
		for _, raw := range pks {
			id, payload, err := splitPacket(raw)
			if err != nil {
				return err
			}
			switch id {
			case packet.IDResourcePackClientResponse:
				var pk packet.ResourcePackClientResponse
				if err := decodeInto(&pk, payload); err != nil {
					return err
				}
				switch pk.Response {
				case packet.PackResponseRefused:
					return errors.New("the client refused the resource packs")
				case packet.PackResponseSendPacks:
					for _, key := range pk.PacksToDownload {
						p := s.n.pack(key)
						if p == nil {
							return fmt.Errorf("client asked for unknown pack %s", key)
						}
						s.sendClient(&packet.ResourcePackDataInfo{
							UUID: key, DataChunkSize: packChunkSize,
							ChunkCount: uint32((len(p.data) + packChunkSize - 1) / packChunkSize),
							Size:       uint64(len(p.data)), Hash: p.hash, PackType: packet.ResourcePackTypeResources,
						})
					}
				case packet.PackResponseAllPacksDownloaded:
					s.sendClient(u.packStack)
				case packet.PackResponseCompleted:
					return nil
				}
			case packet.IDResourcePackChunkRequest:
				var pk packet.ResourcePackChunkRequest
				if err := decodeInto(&pk, payload); err != nil {
					return err
				}
				p := s.n.pack(pk.UUID)
				if p == nil {
					return fmt.Errorf("client asked for a chunk of unknown pack %s", pk.UUID)
				}
				off := int(pk.ChunkIndex) * packChunkSize
				if off < 0 || off >= len(p.data) {
					return fmt.Errorf("pack %s chunk %d out of range", pk.UUID, pk.ChunkIndex)
				}
				end := min(off+packChunkSize, len(p.data))
				s.sendClient(&packet.ResourcePackChunkData{UUID: pk.UUID, ChunkIndex: uint32(pk.ChunkIndex), DataOffset: uint64(off), Data: p.data[off:end]})
			case packet.IDClientCacheStatus:
				// Backends are always told the client has no blob cache (see openLink).
			}
		}
	}
}

// openLink dials a server and logs the player in; the link's reader handles its pack screen and
// reports StartGame by closing l.started.
func (s *switchSession) openLink(server ServerConfig) (*link, error) {
	ctx, cancel := context.WithTimeout(s.ctx, backendDialTimeout)
	defer cancel()
	c, conn, err := s.n.dialServer(ctx, server, server.backendProtocol(s.proto), s.id, s.cd, s.conn.RemoteAddr().String(), s.backendLoginToken())
	if err != nil {
		return nil, err
	}
	l := &link{server: server, c: c, conn: conn, started: make(chan struct{}), closed: make(chan struct{})}
	go s.readLogin(l)
	return l, nil
}

// readLogin plays the client's side of a backend's join up to StartGame, then hands over to
// readBackend (for a switch) or leaves it to join (for the first server).
func (s *switchSession) readLogin(l *link) {
	defer s.recoverSession("readLogin")
	for {
		pks, err := l.c.ReadPackets()
		if err != nil {
			l.err = err
			l.close()
			return
		}
		for i, raw := range pks {
			id, payload, err := splitPacket(raw)
			if err != nil {
				l.err = err
				l.close()
				return
			}
			switch id {
			case packet.IDPlayStatus:
				var pk packet.PlayStatus
				_ = decodeInto(&pk, payload)
				if pk.Status == packet.PlayStatusLoginSuccess {
					_ = l.c.WritePacket(&packet.ClientCacheStatus{Enabled: false})
					continue
				}
				if pk.Status != packet.PlayStatusPlayerSpawn {
					l.err = fmt.Errorf("login refused (status %d)", pk.Status)
					l.close()
					return
				}
			case packet.IDDisconnect:
				var pk packet.Disconnect
				_ = decodeInto(&pk, payload)
				l.err = errors.New(text.Clean(pk.Message))
				l.close()
				return
			case packet.IDResourcePacksInfo:
				s.record(l.server.Name, id, payload)
				_ = l.c.WritePacket(&packet.ResourcePackClientResponse{Response: packet.PackResponseAllPacksDownloaded})
				continue
			case packet.IDResourcePackStack:
				s.record(l.server.Name, id, payload)
				_ = l.c.WritePacket(&packet.ResourcePackClientResponse{Response: packet.PackResponseCompleted})
				continue
			case packet.IDStartGame:
				sg := &packet.StartGame{}
				if err := decodeInto(sg, payload); err != nil {
					l.err = err
					l.close()
					return
				}
				s.record(l.server.Name, id, payload)
				l.startGame = sg
				// The rest of this batch belongs to the game: queue it for readBackend.
				l.c.Requeue(pks[i+1:])
				close(l.started)
				return
			case packet.IDItemRegistry, packet.IDAvailableActorIdentifiers, packet.IDBiomeDefinitionList:
				// Registries: the client gets the union's, never a server's own.
				if id == packet.IDItemRegistry && l.itemReg != nil {
					continue
				}
				s.record(l.server.Name, id, payload)
				if id == packet.IDItemRegistry {
					reg := &packet.ItemRegistry{}
					if decodeInto(reg, payload) == nil {
						stripItemData(reg)
						l.itemReg = reg
					}
				}
			default:
				l.pre = append(l.pre, raw)
			}
		}
	}
}

// readBackend pumps a link's packets to the client for as long as it is the player's server (or the
// one they are moving to). first is the link of the first join, whose registry packets were already
// replaced by the union's.
func (s *switchSession) readBackend(l *link, first bool) {
	defer s.recoverSession("readBackend")
	defer func() {
		l.close()
		s.backendGone(l)
	}()
	var out [][]byte
	for {
		pks, err := l.c.ReadPackets()
		if err != nil {
			if l.err == nil {
				l.err = err
			}
			return
		}
		out = out[:0] // forward is done with it: queues copy, and writes are synchronous
		for _, raw := range pks {
			id, payload, err := splitPacket(raw)
			if err != nil {
				l.err = err
				return
			}
			sub := subClientOf(raw)
			s.debugPacket("server", headerValue(raw), id, payload)
			if sub != 0 && s.host == nil { // a passed-through guest's packet
				if rw := s.passGuestToClient(l, raw, id, payload); rw != nil {
					out = append(out, rw)
				}
				continue
			}
			switch id {
			case packet.IDItemRegistry, packet.IDAvailableActorIdentifiers, packet.IDBiomeDefinitionList:
				// Only a server's first item list is its registry: Dragonfly sends a second, short
				// one (its custom items again) after the spawn, which must not replace it.
				if id == packet.IDItemRegistry && l.itemReg != nil {
					continue
				}
				s.record(l.server.Name, id, payload)
				if id == packet.IDItemRegistry {
					reg := &packet.ItemRegistry{}
					if decodeInto(reg, payload) == nil {
						stripItemData(reg)
						l.itemReg = reg
						s.setItems(l)
					}
				}
				if id == packet.IDBiomeDefinitionList && first {
					break // the first server's biomes are the client's
				}
				continue
			case packet.IDDisconnect:
				var pk packet.Disconnect
				_ = decodeInto(&pk, payload)
				l.err = &kickError{msg: pk.Message}
				return
			case packet.IDPlayStatus:
				var pk packet.PlayStatus
				_ = decodeInto(&pk, payload)
				if pk.Status == packet.PlayStatusPlayerSpawn {
					s.onSpawn(l)
					if !first {
						continue // the client already spawned once
					}
				}
			case packet.IDRespawn:
				s.observeRespawn(l, payload)
			case packet.IDAvailableCommands:
				raw = s.injectCommands(raw, payload)
			}
			rw, err := l.rw.rewrite(raw, true, s.pool)
			if err != nil {
				s.log.Debug("network: could not rewrite a packet, dropped", "id", id, "err", err)
				continue
			}
			out = append(out, rw)
		}
		if !s.forward(l, out) {
			return
		}
	}
}

type kickError struct{ msg string }

func (k *kickError) Error() string { return "kicked: " + text.Clean(k.msg) }

// forward passes a link's packets on if it is the player's server, or queues them while the link is
// the target of a move that is not wired yet. It reports false once the link is abandoned.
func (s *switchSession) forward(l *link, pks [][]byte) bool {
	if s.scrub || opFilterDebug {
		for i, raw := range pks {
			out := scrubForPlayer(raw)
			if len(out) == 0 || len(raw) == 0 || &out[0] == &raw[0] {
				continue
			}
			if s.scrub {
				pks[i] = withSub(raw, out)
			}
			s.noteScrubbed(raw)
		}
	}
	s.mu.Lock()
	if t := s.transfer; t != nil && t.l == l && t.queueing {
		t.queue = append(t.queue, pks...)
		s.mu.Unlock()
		return true
	}
	if s.cur != l {
		s.mu.Unlock()
		return false
	}
	moving := s.transfer
	for _, raw := range pks {
		if subClientOf(raw) != 0 {
			continue // a guest's packets (passthrough) are not the host's
		}
		s.track(raw)
		if moving != nil && moving.l == l && moving.inPhase2() {
			moving.rememberChunk(raw)
		}
		s.observeSettle(l, raw) // see dimreplay.go
	}
	s.mu.Unlock()
	if len(pks) == 0 {
		return true
	}
	if err := s.writeClient(pks...); err != nil {
		s.cancel(fmt.Errorf("write to client: %w", err))
		return false
	}
	return true
}

// backendGone handles a link ending. The player's own server ending sends them to the default server
// (when it is another one) or disconnects them with the server's reason.
func (s *switchSession) backendGone(l *link) {
	s.mu.Lock()
	t := s.transfer
	cur := s.cur
	s.mu.Unlock()
	if t != nil && t.l == l {
		if !t.reattach {
			t.fail(s, "the server closed the connection: "+errText(l.err))
			return
		}
		t.abandon(s) // a held player's server fell over again while they were put back on it
	}
	if cur != l || s.ctx.Err() != nil {
		return
	}
	reason := errText(l.err)
	var kick *kickError
	if errors.As(l.err, &kick) {
		reason = kick.msg
	}
	if s.startHold(l, reason) {
		return // a server with a hold that went down: the player waits for it (hold.go)
	}
	s.moveAway(l.server.Name, reason)
}

// moveAway sends a player whose server let them go for reason to another one. Like Velocity's
// server list: the player goes to the first other server (the default one first), for example
// while their server restarts. Only with none left are they disconnected.
func (s *switchSession) moveAway(from, reason string) {
	for _, other := range s.n.orderedServers() {
		if strings.EqualFold(other.Name, from) {
			continue
		}
		s.log.Info("network: server ended the connection, moving the player to another server", "server", from, "to", other.Name, "reason", reason)
		if err := s.requestSwitch(other, reason); err == nil {
			return
		}
	}
	s.disconnect(reason)
}

func errText(err error) string {
	if err == nil {
		return "connection closed"
	}
	if errors.Is(err, net.ErrClosed) {
		return "connection closed"
	}
	return err.Error()
}

// readClient pumps the client's packets to its server.
func (s *switchSession) readClient() {
	defer s.recoverSession("readClient")
	for {
		pks, err := s.readPackets()
		if err != nil {
			s.cancel(fmt.Errorf("client: %w", err))
			return
		}
		if s.host == nil {
			pks = s.routeSubClients(pks) // split-screen players' packets go to their own sessions
		}
		if s.packets.over(len(pks)) {
			s.log.Warn("player disconnected: packet rate limit", "packets", len(pks))
			s.disconnect(tooManyPacketsMessage)
			return
		}
		s.mu.Lock()
		cur, t, held := s.cur, s.transfer, s.hold != nil
		s.mu.Unlock()
		out := make([][]byte, 0, len(pks))
		for _, raw := range pks {
			id, payload, err := splitPacket(raw)
			if err != nil {
				s.cancel(err)
				return
			}
			sub := clientSubClient(raw)
			s.debugPacket("client", headerValue(raw), id, payload)
			if sub != 0 && s.host == nil { // only passed-through guests' packets are left here
				if held || (t != nil && t.l != cur) {
					continue
				}
				if rw := s.passGuestToServer(cur, raw, id); rw != nil {
					out = append(out, rw)
				}
				continue
			}
			if id != packet.IDPlayerAuthInput {
				s.logSettlingPacket(id)
			}
			switch id {
			case packet.IDClientCacheStatus:
				continue
			case packet.IDRequestChunkRadius:
				var pk packet.RequestChunkRadius
				if decodeInto(&pk, payload) == nil {
					s.mu.Lock()
					s.chunkRadius, s.maxRadius = pk.ChunkRadius, pk.MaxChunkRadius
					first := !s.radiusSeen
					s.radiusSeen = true
					s.mu.Unlock()
					if s.unchecked || Checks == nil {
						break
					}
					if kick := Checks.ViewDistance(s.id, s.cd, pk.ChunkRadius, pk.MaxChunkRadius, first, s.log); kick != "" {
						s.disconnect(kick)
						return
					}
				}
			case packet.IDPlayerAuthInput:
				if s.settling.Load() && s.unsettled(payload) {
					continue
				}
			case packet.IDPlayerAction:
				var pk packet.PlayerAction
				if decodeInto(&pk, payload) == nil {
					if pk.ActionType == protocol.PlayerActionDimensionChangeDone && t != nil && t.dimChangeDone(s) {
						continue
					}
					if pk.ActionType == protocol.PlayerActionRespawn && s.takeProxyRespawn() {
						s.sendClient(&packet.ActorEvent{EntityRuntimeID: s.clientRID, EventType: actorEventRespawn})
						continue
					}
				}
			case packet.IDRespawn:
				s.mu.Lock()
				pr := s.proxyRespawn
				s.mu.Unlock()
				if pr {
					continue
				}
			case packet.IDSubChunkRequest:
				// Mid-move the requests are for the relay's own empty chunks; and a server that
				// doesn't use request mode kicks a client that sends one. Both get air from here.
				if (t != nil && t.inPhase1()) || (cur != nil && !s.n.usesRequestMode(cur.server.Name)) {
					var pk packet.SubChunkRequest
					if decodeInto(&pk, payload) == nil {
						s.mu.Lock()
						dim := s.dimension
						s.mu.Unlock()
						if (t == nil || !t.inPhase1()) && pk.Dimension == dim {
							// For the dimension the player is in, on a server that sends whole
							// columns: answered from those columns, see subserve.go.
							s.serveSubChunks(&pk)
							continue
						}
						s.sendClient(airSubChunks(&pk))
						if false {
							// A request for the dimension the player is in now, on a server that
							// sends columns whole: the client still has a column of the server it
							// came from at that place (two lobbies at the same coordinates). It
							// waits for an answer, and the air wipes the new server's column:
							// that one is sent again, see wiped in dimreplay.go.
							s.noteWiped(cur, &pk)
							s.log.Debug("network: sub-chunk request on a whole-column server", "base", pk.Position, "offsets", len(pk.Offsets))
						}
					}
					continue
				}
				if req := new(packet.SubChunkRequest); decodeInto(req, payload) == nil {
					rest := s.placeholderSubChunks(req) // the relay's own columns: placeholders.go
					if rest == nil {
						continue
					}
					if rest != req {
						raw = encodePacket(rest)
					}
				}
			case packet.IDContainerClose:
				var pk packet.ContainerClose
				if decodeInto(&pk, payload) == nil {
					s.mu.Lock()
					delete(s.containers, pk.WindowID)
					s.mu.Unlock()
				}
			case packet.IDText:
				var pk packet.Text
				if decodeInto(&pk, payload) == nil && pk.TextType == packet.TextTypeChat && (s.mutedNow() || s.derivedID && s.host.mutedNow()) {
					continue
				}
			case packet.IDCommandRequest:
				var pk packet.CommandRequest
				if decodeInto(&pk, payload) == nil && (s.mutedCommand(pk.CommandLine) || s.handleModeration(pk.CommandLine) || s.handleCommand(pk.CommandLine)) {
					s.noteStaffCommand(pk.CommandLine)
					continue
				}
				// A bare /geyser is admin-only in Geyser: show players its help instead.
				if strings.EqualFold(strings.TrimSpace(pk.CommandLine), "/geyser") {
					pk.CommandLine = "/geyser help"
					raw = encodePacket(&pk)
				}
			}
			if cur == nil || (t != nil && t.l != cur) || held {
				continue // mid-move, or held while the server is down (hold.go): nothing to send to yet
			}
			rw, err := cur.rw.rewrite(raw, false, s.pool)
			if err != nil {
				continue
			}
			out = append(out, rw)
		}
		if cur != nil && len(out) > 0 {
			if err := cur.c.WritePackets(out); err != nil {
				// The backend side notices and handles its own end.
				continue
			}
		}
	}
}

func (s *switchSession) takeProxyRespawn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.proxyRespawn {
		return false
	}
	s.proxyRespawn = false
	return true
}

// onSpawn handles a backend's PlayStatus PlayerSpawn.
func (s *switchSession) onSpawn(l *link) {
	s.mu.Lock()
	l.spawned = true
	t := s.transfer
	s.mu.Unlock()
	if t != nil && t.l == l {
		t.onPlayStatus(s)
		return
	}
	// The first join: the client sends SetLocalPlayerAsInitialised itself.
}

func (s *switchSession) observeRespawn(l *link, payload []byte) {
	var pk packet.Respawn
	if decodeInto(&pk, payload) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.Debug("network: respawn packet from server", "state", pk.State, "server", l.server.Name, "current", s.cur == l, "position", pk.Position)
	switch pk.State {
	case packet.RespawnStateSearchingForSpawn:
		if s.cur == l {
			s.dead = true
		}
	case packet.RespawnStateReadyToSpawn:
		if s.cur == l {
			s.dead = false
		}
		if t := s.transfer; t != nil && t.l == l {
			t.spawn = pk.Position
		}
	}
}

// track updates what the client has from the server, from a packet as the client gets it. s.mu held.
func (s *switchSession) track(raw []byte) {
	id, payload, err := splitPacket(raw)
	if err != nil {
		return
	}
	switch id {
	case packet.IDAddActor:
		var pk packet.AddActor
		if decodeInto(&pk, payload) == nil {
			s.entities[pk.EntityUniqueID] = true
			for _, lk := range pk.EntityLinks {
				s.trackLink(lk)
			}
		}
	case packet.IDAddPlayer:
		var pk packet.AddPlayer
		if decodeInto(&pk, payload) == nil {
			s.entities[pk.AbilityData.EntityUniqueID] = true
		}
	case packet.IDAddItemActor:
		var pk packet.AddItemActor
		if decodeInto(&pk, payload) == nil {
			s.entities[pk.EntityUniqueID] = true
		}
	case packet.IDAddPainting:
		var pk packet.AddPainting
		if decodeInto(&pk, payload) == nil {
			s.entities[pk.EntityUniqueID] = true
		}
	case packet.IDRemoveActor:
		var pk packet.RemoveActor
		if decodeInto(&pk, payload) == nil {
			delete(s.entities, pk.EntityUniqueID)
			delete(s.links, pk.EntityUniqueID)
		}
	case packet.IDSetActorLink:
		var pk packet.SetActorLink
		if decodeInto(&pk, payload) == nil {
			s.trackLink(pk.EntityLink)
		}
	case packet.IDPlayerList:
		var pk packet.PlayerList
		if decodeInto(&pk, payload) == nil {
			for _, e := range pk.Entries {
				if e.ActionType == protocol.PlayerListActionAdd {
					s.players[e.UUID] = true
				} else {
					delete(s.players, e.UUID)
				}
			}
		}
	case packet.IDSetDisplayObjective:
		var pk packet.SetDisplayObjective
		if decodeInto(&pk, payload) == nil {
			s.objectives[pk.ObjectiveName] = true
		}
	case packet.IDRemoveObjective:
		var pk packet.RemoveObjective
		if decodeInto(&pk, payload) == nil {
			delete(s.objectives, pk.ObjectiveName)
		}
	case packet.IDSetScore:
		var pk packet.SetScore
		if decodeInto(&pk, payload) == nil {
			for _, e := range pk.Entries {
				if e.IdentityType == protocol.ScoreboardIdentityRemove {
					delete(s.scores, e.EntryID)
				} else {
					s.scores[e.EntryID] = e
				}
			}
		}
	case packet.IDLocatorBar:
		var pk packet.LocatorBar
		if decodeInto(&pk, payload) == nil {
			for _, w := range pk.Waypoints {
				switch w.Action {
				case protocol.WaypointActionAdd:
					s.waypoints[w.GroupHandle] = true
				case protocol.WaypointActionRemove:
					delete(s.waypoints, w.GroupHandle)
				}
			}
		}
	case packet.IDBossEvent:
		var pk packet.BossEvent
		if decodeInto(&pk, payload) == nil {
			switch pk.EventType {
			case packet.BossEventShow:
				s.bossBars[pk.BossEntityUniqueID] = true
			case packet.BossEventHide:
				delete(s.bossBars, pk.BossEntityUniqueID)
			}
		}
	case packet.IDContainerOpen:
		var pk packet.ContainerOpen
		if decodeInto(&pk, payload) == nil {
			s.containers[pk.WindowID] = pk.ContainerType
		}
	case packet.IDContainerClose:
		var pk packet.ContainerClose
		if decodeInto(&pk, payload) == nil {
			delete(s.containers, pk.WindowID)
		}
	case packet.IDPlayerFog:
		var pk packet.PlayerFog
		if decodeInto(&pk, payload) == nil {
			s.fog = len(pk.Stack) > 0
		}
	case packet.IDUpdateClientInputLocks:
		var pk packet.UpdateClientInputLocks
		if decodeInto(&pk, payload) == nil {
			s.inputLocks = pk.Locks
		}
	case packet.IDSetHud:
		var pk packet.SetHud
		if decodeInto(&pk, payload) == nil {
			for _, e := range pk.Elements {
				if pk.Visibility == packet.HudVisibilityHide {
					s.hiddenHud[e] = true
				} else {
					delete(s.hiddenHud, e)
				}
			}
		}
	case packet.IDNetworkChunkPublisherUpdate:
		s.notePublisher(payload)
	case packet.IDChangeDimension:
		var pk packet.ChangeDimension
		if decodeInto(&pk, payload) == nil {
			s.dimension = pk.Dimension
			s.forgetColumns()
		}
	case packet.IDSubChunk:
		if s.awaitGround == nil {
			return
		}
		var pk packet.SubChunk
		if decodeInto(&pk, payload) != nil {
			return
		}
		for _, e := range pk.SubChunkEntries {
			col := [2]int32{pk.Position[0] + int32(e.Offset[0]), pk.Position[2] + int32(e.Offset[2])}
			if col == s.awaitCol && (e.Result == protocol.SubChunkResultSuccess || e.Result == protocol.SubChunkResultSuccessAllAir) {
				close(s.awaitGround) // the ground at the spawn is on its way to the client
				s.awaitGround = nil
				break
			}
		}
	case packet.IDLevelChunk:
		// Only the head is read: the blocks are kept raw (keepColumn) and never decoded here.
		if h, ok := readChunkHead(payload); ok {
			pk := packet.LevelChunk{Position: h.pos, CacheEnabled: h.cache}
			if h.requestMode {
				pk.SubChunkLimit = protocol.Option(h.limit)
			}
			delete(s.placeholders, [2]int32{pk.Position[0], pk.Position[1]}) // the server's own chunk now
			s.keepColumn(&pk, raw)
			_, s.subChunkMode = pk.SubChunkLimit.Value()
			if s.cur != nil {
				s.n.requestMode.Store(s.cur.server.Name, s.subChunkMode)
			}
		}
	case packet.IDUpdateAttributes:
		if v, ok := peekVaruint64(payload); !ok || v != s.clientRID {
			return
		}
		var pk packet.UpdateAttributes
		if decodeInto(&pk, payload) == nil {
			for _, a := range pk.Attributes {
				if a.Name == "minecraft:health" {
					if (a.Value <= 0) != s.dead {
						s.log.Info("network: health seen", "value", a.Value, "max", a.Max, "server", nameOf(s.cur))
					}
					s.dead = a.Value <= 0
				}
			}
		}
	}
}

func (s *switchSession) trackLink(lk protocol.EntityLink) {
	if lk.Type == protocol.EntityLinkRemove {
		delete(s.links, lk.RiddenEntityUniqueID)
	} else {
		s.links[lk.RiddenEntityUniqueID] = lk.RiderEntityUniqueID
	}
}

// requestSwitch moves the player to a server in the background. reason, when set, is why their
// server sent them away (shown to them).
func (s *switchSession) requestSwitch(server ServerConfig, reason string) error {
	if err := s.checkSwitch(server, reason); err != nil {
		return err
	}
	go s.switchTo(server, reason)
	return nil
}

// checkSwitch reports why the player can't be moved to server now, if so.
func (s *switchSession) checkSwitch(server ServerConfig, reason string) error {
	if !server.Accepts(s.proto) {
		return errors.New(server.versionMessage(s.proto))
	}
	s.mu.Lock()
	if s.cur == nil {
		// Still joining (on the resource pack screen): the join holds its own login to a
		// server, and a second one is refused as a duplicate. The join itself picks the server.
		s.mu.Unlock()
		return errors.New("still joining")
	}
	if s.transfer != nil {
		s.mu.Unlock()
		return errors.New("already being moved to another server")
	}
	if s.cur != nil && strings.EqualFold(s.cur.server.Name, server.Name) && reason == "" {
		s.mu.Unlock()
		return fmt.Errorf("already on %s", server.Name)
	}
	s.mu.Unlock()
	return nil
}

func registerBackendPingIf(xuid string, conn backendConn) {
	if r, ok := conn.(latencyReporter); ok {
		registerBackendPing(xuid, r)
	}
}

var _ = mgl32.Vec3{}

// setItems builds a link's item renumbering from its server's item list, if it came.
// clientBlockItems is the client's custom block items: item id -> the client runtime id of the
// block with the same name (the default, first state).
func (s *switchSession) clientBlockItems() map[int32]uint32 {
	out := map[int32]uint32{}
	first := map[string]uint32{}
	for i, st := range s.palette.states {
		if _, ok := first[st.Name]; !ok {
			first[st.Name] = uint32(i)
		}
	}
	for _, name := range s.palette.custom {
		if id, ok := s.itemIDs[name]; ok {
			out[id] = first[name]
		}
	}
	return out
}

// stripItemData drops a server's item NBT: only names and numbers are used (newItemMap), and the
// data is most of the list's size, kept once per player.
func stripItemData(reg *packet.ItemRegistry) {
	for i := range reg.Items {
		reg.Items[i].Data = nil
	}
}

func (s *switchSession) setItems(l *link) {
	if l.itemReg == nil || l.rw == nil {
		return
	}
	if l.rw.blockItems == nil {
		l.rw.blockItems = s.clientBlockItems()
	}
	m := newItemMap(l.itemReg.Items, s.itemIDs)
	l.rw.items.Store(m)
	var shields [2]int32
	for _, it := range l.itemReg.Items {
		if it.Name == "minecraft:shield" {
			shields[0] = int32(it.RuntimeID)
		}
	}
	shields[1] = s.itemIDs["minecraft:shield"]
	l.rw.shields.Store(&shields)
	if m != nil {
		s.log.Info("network: items renumbered for this server", "server", l.server.Name)
	}
}

// awaitSpawnColumnLocked returns a channel closed once the server's own sub-chunks for the column
// of pos have passed to the client. s.mu held.
func (s *switchSession) awaitSpawnColumnLocked(pos mgl32.Vec3) chan struct{} {
	ch := make(chan struct{})
	s.awaitGround = ch
	s.awaitCol = [2]int32{int32(math.Floor(float64(pos[0]))) >> 4, int32(math.Floor(float64(pos[2]))) >> 4}
	return ch
}

// withFirstServer puts the server named first at the front of order: moved there when order has
// it, added when it is one of the network's servers that order leaves out (try lists only where
// players go by default) and takes protocol id. Unknown, or not taking id: order as it is.
func (n *Network) withFirstServer(order []ServerConfig, first string, id int32) []ServerConfig {
	if first == "" {
		return order
	}
	for _, s := range order {
		if strings.EqualFold(s.Name, first) {
			return firstServerFirst(order, first)
		}
	}
	if s := n.conf().server(first); s != nil && s.Accepts(id) {
		return append([]ServerConfig{*s}, order...)
	}
	return order
}

// firstServerFirst moves the server named first to the front of order, if it is there.
func firstServerFirst(order []ServerConfig, first string) []ServerConfig {
	for i, s := range order {
		if first != "" && strings.EqualFold(s.Name, first) {
			out := append([]ServerConfig{s}, order[:i]...)
			return append(out, order[i+1:]...)
		}
	}
	return order
}

// serverNames lists the servers' names, for the logs.
func serverNames(servers []ServerConfig) []string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.Name
	}
	return out
}
