package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Chat of the chosen servers goes to a Discord channel through a webhook (beacon.toml
// discord-webhook, discord-servers). Lines come from the chat hub, so a server needs to send its
// chat there. They are posted a few at a time: Discord limits how often a webhook may post.

const (
	discordEvery   = 2 * time.Second
	discordMaxLen  = 1900 // of Discord's 2000 a message
	discordBacklog = 256
)

var discordFormat = regexp.MustCompile(`§.|</?[a-zA-Z][^<>]*>`)

// discordMarkup are the characters Discord would read as formatting.
var discordMarkup = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, `~`, `\~`, "`", "\\`", `|`, `\|`, `>`, `\>`)

// discordLine is a chat line as it reads in Discord: no colours, nothing taken for formatting.
func discordLine(server, text string) string {
	text = strings.TrimSpace(discordFormat.ReplaceAllString(text, ""))
	return "**[" + discordMarkup.Replace(server) + "]** " + discordMarkup.Replace(text)
}

type discordBridge struct {
	mu      sync.Mutex
	url     string
	servers []string
	started bool
	lines   chan string
	client  *http.Client
	every   time.Duration
}

var discord = &discordBridge{lines: make(chan string, discordBacklog), client: &http.Client{Timeout: 10 * time.Second}, every: discordEvery}

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

// add queues a line if its server is sent. A full queue drops it, rather than hold up chat.
func (d *discordBridge) add(server, text string) {
	d.mu.Lock()
	on := d.url != "" && (len(d.servers) == 0 || slices.ContainsFunc(d.servers, func(s string) bool { return strings.EqualFold(s, server) }))
	d.mu.Unlock()
	if !on {
		return
	}
	select {
	case d.lines <- discordLine(server, text):
	default:
	}
}

// run posts what is queued, every d.every.
func (d *discordBridge) run() {
	var carry string
	for range time.Tick(d.every) {
		body := carry
		carry = ""
	fill:
		for {
			select {
			case l := <-d.lines:
				if len(l) > discordMaxLen {
					l = strings.ToValidUTF8(l[:discordMaxLen], "")
				}
				if len(body)+len(l)+1 > discordMaxLen {
					carry = l
					break fill
				}
				if body != "" {
					body += "\n"
				}
				body += l
			default:
				break fill
			}
		}
		if body != "" {
			d.post(body)
		}
	}
}

// post sends one message, waiting out and retrying once if Discord asks for that. The webhook's
// address is a secret: it is never logged.
func (d *discordBridge) post(content string) {
	d.mu.Lock()
	url := d.url
	d.mu.Unlock()
	if url == "" {
		return
	}
	// No parse entries: nothing a player types can ping anyone.
	data, _ := json.Marshal(map[string]any{"content": content, "allowed_mentions": map[string]any{"parse": []string{}}})
	for range 2 {
		resp, err := d.client.Post(url, "application/json", bytes.NewReader(data))
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
