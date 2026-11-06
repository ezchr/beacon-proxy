package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// runConsole reads commands typed into Beacon's console (stdin), like Velocity's console. It
// returns when stdin closes, which it is when Beacon runs as a service.
func runConsole(ctx context.Context, in io.Reader, stop context.CancelFunc, log *slog.Logger) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		for _, line := range consoleCommand(fields, stop) {
			fmt.Println(line)
		}
	}
	log.Debug("console input closed")
}

// controlCommand runs an invites/friendadd console command through the control API's routes.
func controlCommand(fields []string) []string {
	if controlSrv == nil {
		return []string{"The control API is off, so invites and friend requests are too."}
	}
	var path string
	switch {
	case fields[0] == "invites" && len(fields) == 2 && (fields[1] == "start" || fields[1] == "stop" || fields[1] == "status"):
		path = "/invites/" + fields[1]
	case fields[0] == "friendadd" && len(fields) == 2 && (fields[1] == "stop" || fields[1] == "status"):
		path = "/friends/add/" + fields[1]
	case fields[0] == "friendadd" && len(fields) >= 2:
		path = "/friends/add/start?from=" + url.QueryEscape(strings.Join(fields[1:], " "))
	default:
		return []string{"Usage: invites start|stop|status   or   friendadd <server or world>|stop|status"}
	}
	rec := httptest.NewRecorder()
	controlSrv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
}

func consoleCommand(fields []string, stop context.CancelFunc) []string {
	switch strings.ToLower(strings.TrimPrefix(fields[0], "/")) {
	case "end", "stop", "shutdown":
		stop()
		return []string{"Shutting down."}
	case "glist":
		if network == nil {
			return []string{"glist needs mode = \"relay\"."}
		}
		return network.GList()
	case "send":
		if network == nil {
			return []string{"send needs mode = \"relay\"."}
		}
		if len(fields) != 3 {
			return []string{"Usage: send <player|all> <server>   (from a server: send current:<server> <server>)"}
		}
		moved, err := network.Send(fields[1], fields[2])
		if err != nil {
			return []string{err.Error()}
		}
		return []string{fmt.Sprintf("Moving %d player(s) to %s.", moved, fields[2])}
	case "broadcast":
		if listing == nil {
			return []string{"The Friends-tab listing switch is off: set listing-toggle = true under [broadcast]."}
		}
		if len(fields) == 2 && (fields[1] == "on" || fields[1] == "off") {
			if err := listing.Set(fields[1] == "on"); err != nil {
				return []string{err.Error()}
			}
		}
		if listing.On() {
			return []string{"The world is listed in the Friends tab."}
		}
		return []string{"The world is not listed in the Friends tab."}
	case "reload":
		return reloadConfig()
	case "ban", "banip", "mute", "unban", "unbanip", "unmute", "banlist", "mutelist", "admin":
		if network == nil {
			return []string{fields[0] + " needs mode = \"relay\"."}
		}
		return network.Moderate(strings.Join(fields, " "))
	case "debug":
		return bridge.DebugCommand(strings.Join(fields[1:], " "))
	case "invites", "friendadd":
		return controlCommand(fields)
	case "help", "?":
		return []string{
			"end                       stop Beacon",
			"glist                     players on every server",
			"send <player|all> <srv>   move players to a server",
			"broadcast [on|off]        show or hide the world in the Friends tab",
			"reload                    reread beacon.toml without a restart",
			"debug                     log packets to a file: type it for how",
			"ban, banip, mute          and unban, unbanip, unmute, banlist, mutelist",
			"admin add|remove <player> make or unmake a Beacon admin; admin list",
			"invites start|stop|status Xbox invites to the players beacon scrape found",
			"friendadd <from>|stop|status  friend requests to the players seen on <from>",
		}
	}
	return []string{fmt.Sprintf("Unknown command %q. Type help.", fields[0])}
}
