package main

import (
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// admins and moderators in beacon.toml are XUIDs, which say nothing to a reader: Beacon keeps a
// "# gamertag" after each, from the players it has seen, and updates it when one of them joins
// under a new gamertag. Only those two lists are rewritten, one XUID a line; an XUID Beacon has not
// seen keeps the comment it has.

var (
	staffListRE  = regexp.MustCompile(`(?ms)^(admins|moderators)[ \t]*=[ \t]*\[(.*?)\]`)
	staffEntryRE = regexp.MustCompile(`"([^"]*)"[ \t]*,?[ \t]*(?:#[ \t]*([^\n]*))?`)
	staffMu      sync.Mutex
)

// annotateStaffNames rewrites the admins and moderators lists of the beacon.toml at path with each
// XUID's gamertag; override names one XUID (a player who just joined).
func annotateStaffNames(path, overrideXUID, overrideName string) {
	staffMu.Lock()
	defer staffMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	text := string(data)
	out := staffListRE.ReplaceAllStringFunc(text, func(block string) string {
		m := staffListRE.FindStringSubmatch(block)
		key, body := m[1], m[2]
		var lines []string
		for _, e := range staffEntryRE.FindAllStringSubmatch(body, -1) {
			id, comment := strings.TrimSpace(e[1]), strings.TrimSpace(e[2])
			if id == "" {
				continue
			}
			name := ""
			if id == overrideXUID {
				name = overrideName
			} else if network != nil {
				name = network.NameOfXUID(id)
			}
			if name == "" {
				name = comment
			}
			line := "  " + tomlQuote(id) + ","
			if name != "" {
				line += " # " + name
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return key + " = []"
		}
		return key + " = [\n" + strings.Join(lines, "\n") + "\n]"
	})
	if out == text {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(out), 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func init() {
	bridge.StaffJoined = func(xuid, name string) { annotateStaffNames(tomlConfigFile, xuid, name) }
}
