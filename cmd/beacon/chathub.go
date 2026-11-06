package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The network's shared chat: each server of the network sends its chat lines here (already
// formatted, with rank and name) and long-polls for the lines of the other servers, which it shows
// with a tag for the server they came from. Served on the control routes (loopback only).
//
//	POST /chat/send   {"server": "pvp", "text": "<formatted line>"}, and optionally "player",
//	                  "xuid" and "message": who said what, for Discord
//	GET  /chat/poll?server=pvp&after=<id>   lines from the other servers after id, waiting up to
//	                                        25 s for one; after=-1 returns only the current id.

const (
	chatKeep    = 200
	chatMaxLen  = 1024
	chatPollFor = 25 * time.Second
)

type chatLine struct {
	ID     int64  `json:"id"`
	Server string `json:"server"`
	Text   string `json:"text"`
}

type chatHub struct {
	mu    sync.Mutex
	lines []chatLine
	next  int64
	wake  chan struct{} // closed and replaced on every new line
}

var chat = &chatHub{wake: make(chan struct{})}

// chatEnabled turns the /chat routes on (beacon.toml cross-server-chat); off, they don't exist.
var chatEnabled bool

func (h *chatHub) add(server, text string) {
	h.mu.Lock()
	h.next++
	h.lines = append(h.lines, chatLine{ID: h.next, Server: server, Text: text})
	if len(h.lines) > chatKeep {
		h.lines = h.lines[len(h.lines)-chatKeep:]
	}
	close(h.wake)
	h.wake = make(chan struct{})
	h.mu.Unlock()
}

// chatSenders are the servers seen sending their own chat to /chat/send. A server's own lines are
// the better ones: they have its formatting, and it left out what it refused.
var chatSenders sync.Map

// chatSenderWait is how long a player's line waits for their server to send it itself.
const chatSenderWait = 1500 * time.Millisecond

// relayedChat takes what a relayed player typed. It counts unless the server sends its own chat,
// which Beacon only knows once it has: the first line waits to see.
func relayedChat(server, player, xuid, message string) {
	if _, sends := chatSenders.Load(strings.ToLower(server)); sends {
		return
	}
	time.AfterFunc(chatSenderWait, func() {
		if _, sends := chatSenders.Load(strings.ToLower(server)); sends {
			return
		}
		line := player + ": " + message
		chat.add(server, line)
		discord.add(server, line, player, xuid, message)
	})
}

// since returns the other servers' lines after id, the newest id, and a channel closed on the next line.
func (h *chatHub) since(server string, after int64) ([]chatLine, int64, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []chatLine
	for _, l := range h.lines {
		if l.ID > after && l.Server != server {
			out = append(out, l)
		}
	}
	return out, h.next, h.wake
}

// serveChat handles the /chat routes, reporting whether the path was one.
func serveChat(w http.ResponseWriter, r *http.Request) bool {
	if !chatEnabled {
		return false
	}
	switch r.URL.Path {
	case "/chat/send":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return true
		}
		var in struct {
			Server string `json:"server"`
			Text   string `json:"text"`
			// Who spoke and what they said, for Discord; optional, and the XUID of Bedrock players only.
			Player  string `json:"player"`
			XUID    string `json:"xuid"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil || in.Server == "" || in.Text == "" {
			http.Error(w, "want {server, text}", http.StatusBadRequest)
			return true
		}
		if len(in.Text) > chatMaxLen {
			in.Text = in.Text[:chatMaxLen]
		}
		chatSenders.Store(strings.ToLower(in.Server), true)
		chat.add(in.Server, in.Text)
		discord.add(in.Server, in.Text, in.Player, in.XUID, in.Message)
		w.WriteHeader(http.StatusNoContent)
		return true
	case "/chat/poll":
		server := r.URL.Query().Get("server")
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if server == "" || err != nil {
			http.Error(w, "want server and after", http.StatusBadRequest)
			return true
		}
		lines, last, wake := chat.since(server, after)
		if after >= 0 && len(lines) == 0 {
			select {
			case <-wake:
				lines, last, _ = chat.since(server, after)
			case <-time.After(chatPollFor):
			case <-r.Context().Done():
				return true
			}
		}
		if after < 0 {
			lines = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Last  int64      `json:"last"`
			Lines []chatLine `json:"lines"`
		}{last, lines})
		return true
	}
	return false
}
