package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Chat of the chosen servers goes to a Discord channel through a webhook (beacon.toml
// discord-webhook, discord-servers). Lines come from the chat hub, so a server needs to send its
// chat there. A server that says who spoke gets that player's name and picture on the message: the
// Xbox gamerpic of a Bedrock player, the skin's face of a Java player. A webhook may only post so
// often, so one post goes out at a time, with a player's lines in a row together.

const (
	discordEvery    = 2 * time.Second
	discordMaxLen   = 1900 // of Discord's 2000 a message
	discordBacklog  = 256
	discordPicFor   = 24 * time.Hour
	discordPicAwait = 5 * time.Second
)

var discordFormat = regexp.MustCompile(`§.|</?[a-zA-Z][^<>]*>`)

// discordMarkup are the characters Discord would read as formatting.
var discordMarkup = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, `~`, `\~`, "`", "\\`", `|`, `\|`, `>`, `\>`)

// discordText is chat text as it reads in Discord: no colours, nothing taken for formatting.
func discordText(text string) string {
	return discordMarkup.Replace(strings.TrimSpace(discordFormat.ReplaceAllString(text, "")))
}

// discordName is a player's name as a webhook may use it: Discord refuses names with these words.
func discordName(name string) string {
	name = strings.TrimSpace(discordFormat.ReplaceAllString(name, ""))
	for _, word := range []string{"discord", "clyde"} {
		for i := strings.Index(strings.ToLower(name), word); i >= 0; i = strings.Index(strings.ToLower(name), word) {
			name = name[:i] + strings.Repeat("-", len(word)) + name[i+len(word):]
		}
	}
	if len(name) > 80 {
		name = strings.ToValidUTF8(name[:80], "")
	}
	return name
}

// discordEntry is a line waiting to go out: its text, and who said it if the server told.
type discordEntry struct {
	line, player, xuid string
}

// discordPic is a looked-up gamerpic.
type discordPic struct {
	url  string
	when time.Time
}

type discordBridge struct {
	mu      sync.Mutex
	url     string
	servers []string
	started bool
	lines   chan discordEntry
	client  *http.Client
	every   time.Duration
	// gamerpic finds an Xbox player's picture; nil until a broadcast is signed in.
	gamerpic func(ctx context.Context, xuid string) (string, error)
	pics     map[string]discordPic
}

var discord = &discordBridge{lines: make(chan discordEntry, discordBacklog), client: &http.Client{Timeout: 10 * time.Second}, every: discordEvery}

// set takes the webhook and the servers to send, none being all of them. An empty url turns it off.
func (d *discordBridge) set(url string, servers []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.url, d.servers = strings.TrimSpace(url), servers
	if d.url != "" && !d.started {
		d.started = true
		go d.run()
	}
}

// on reports whether a webhook is set.
func (d *discordBridge) on() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url != ""
}

// setGamerpic gives the bridge its way to find Xbox pictures.
func (d *discordBridge) setGamerpic(f func(ctx context.Context, xuid string) (string, error)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gamerpic = f
}

// add queues a line if its server is sent: text is the whole line, and player and message what a
// server that says who spoke sent. A full queue drops it, rather than hold up chat.
func (d *discordBridge) add(server, text, player, xuid, message string) {
	d.mu.Lock()
	on := d.url != "" && (len(d.servers) == 0 || slices.ContainsFunc(d.servers, func(s string) bool { return strings.EqualFold(s, server) }))
	d.mu.Unlock()
	if !on {
		return
	}
	e := discordEntry{player: discordName(player), xuid: xuid}
	if e.player != "" && message != "" {
		text = message
	} else {
		e.player = ""
	}
	e.line = "**[" + discordMarkup.Replace(server) + "]** " + discordText(text)
	if len(e.line) > discordMaxLen {
		e.line = strings.ToValidUTF8(e.line[:discordMaxLen], "")
	}
	select {
	case d.lines <- e:
	default:
	}
}

// run posts one message every d.every: the next line, with those after it from the same speaker.
func (d *discordBridge) run() {
	var next *discordEntry
	for range time.Tick(d.every) {
		if next == nil {
			select {
			case e := <-d.lines:
				next = &e
			default:
				continue
			}
		}
		first, body := *next, next.line
		next = nil
	fill:
		for {
			select {
			case e := <-d.lines:
				if e.player != first.player || len(body)+len(e.line)+1 > discordMaxLen {
					next = &e
					break fill
				}
				body += "\n" + e.line
			default:
				break fill
			}
		}
		d.post(body, first.player, d.picture(first))
	}
}

// picture is the address of a speaker's picture, or "" for the webhook's own.
func (d *discordBridge) picture(e discordEntry) string {
	if e.player == "" {
		return ""
	}
	if e.xuid == "" {
		return "https://mc-heads.net/avatar/" + url.PathEscape(e.player) + "/128"
	}
	d.mu.Lock()
	pic, ok := d.pics[e.xuid]
	find := d.gamerpic
	d.mu.Unlock()
	if ok && time.Since(pic.when) < discordPicFor {
		return pic.url
	}
	if find == nil {
		return pic.url
	}
	ctx, cancel := context.WithTimeout(context.Background(), discordPicAwait)
	defer cancel()
	found, err := find(ctx, e.xuid)
	if err != nil {
		slog.Debug("discord: no gamerpic", "xuid", e.xuid, "err", err)
		found = pic.url
	}
	d.mu.Lock()
	if d.pics == nil {
		d.pics = map[string]discordPic{}
	}
	d.pics[e.xuid] = discordPic{url: found, when: time.Now()}
	d.mu.Unlock()
	return found
}

// post sends one message, waiting out and retrying once if Discord asks for that. The webhook's
// address is a secret: it is never logged.
func (d *discordBridge) post(content, username, avatar string) {
	d.mu.Lock()
	hook := d.url
	d.mu.Unlock()
	if hook == "" {
		return
	}
	// No parse entries: nothing a player types can ping anyone.
	m := map[string]any{"content": content, "allowed_mentions": map[string]any{"parse": []string{}}}
	if username != "" {
		m["username"] = username
	}
	if avatar != "" {
		m["avatar_url"] = avatar
	}
	data, _ := json.Marshal(m)
	for range 2 {
		resp, err := d.client.Post(hook, "application/json", bytes.NewReader(data))
		if err != nil {
			slog.Warn("discord: could not send chat", "err", "request failed")
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			if resp.StatusCode >= 300 {
				slog.Warn("discord: chat refused", "status", resp.StatusCode)
			}
			return
		}
		wait, _ := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64)
		time.Sleep(time.Duration(min(max(wait, 1), 30) * float64(time.Second)))
	}
}
