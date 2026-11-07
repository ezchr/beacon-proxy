package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ezchr/beacon-proxy/internal/session"
	"github.com/ezchr/beacon-proxy/internal/xbl"
	"golang.org/x/oauth2"
)

// `beacon accounts ...` manages the Xbox accounts that host the Friends-tab world: the main one
// (token.json) and the extra ones under [[broadcast.extra]] in beacon.toml, each adding 30 join
// slots and its own friends list.

const friendLimit = 1000

var accountNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func runAccounts(args []string, log *slog.Logger) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	var err error
	switch args[0] {
	case "list":
		err = accountsList()
	case "add":
		if len(args) != 2 || !accountNameRE.MatchString(args[1]) {
			fmt.Println("Usage: beacon accounts add <name>   (name: letters, digits, - and _)")
			return 2
		}
		err = accountsAdd(args[1], log)
	case "remove":
		if len(args) != 2 {
			fmt.Println("Usage: beacon accounts remove <name>")
			return 2
		}
		err = accountsRemove(args[1])
	default:
		fmt.Println("Usage: beacon accounts [list | add <name> | remove <name>]")
		return 2
	}
	if err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	return 0
}

type account struct{ name, tokenFile string }

func configuredAccounts() ([]account, error) {
	cfg, err := loadBeaconConfig()
	if err != nil {
		return nil, err
	}
	accounts := []account{{"main", tokenCacheFile}}
	for _, b := range cfg.ExtraBroadcasts {
		accounts = append(accounts, account{b.Name, b.TokenFile})
	}
	return accounts, nil
}

// accountIdentity signs in from a cached token only (never prompting) and returns the gamertag,
// XUID and session.
func accountIdentity(ctx context.Context, tokenFile string) (string, string, *session.Session, error) {
	tok, err := readToken(tokenFile)
	if err != nil {
		return "", "", nil, err
	}
	src := &cachingTokenSource{src: deviceAuth.RefreshTokenSource(tok), path: tokenFile}
	return identityFrom(ctx, src)
}

func readToken(file string) (*oauth2.Token, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	tok := new(oauth2.Token)
	if err := json.Unmarshal(b, tok); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return tok, nil
}

func identityFrom(ctx context.Context, src oauth2.TokenSource) (string, string, *session.Session, error) {
	s, err := session.SessionFromTokenSource(src, deviceAuth, ctx)
	if err != nil {
		return "", "", nil, err
	}
	x, err := s.RequestXBLToken(ctx, "http://xboxlive.com")
	if err != nil {
		return "", "", nil, err
	}
	u := x.AuthorizationToken.DisplayClaims.UserInfo[0]
	return u.GamerTag, u.XUID, s, nil
}

func accountsList() error {
	accounts, err := configuredAccounts()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, a := range accounts {
		gamertag, xuid, s, err := accountIdentity(ctx, a.tokenFile)
		if err != nil {
			fmt.Printf("%-12s %-22s not signed in (%v): beacon accounts add %s\n", a.name, a.tokenFile, err, a.name)
			continue
		}
		friends := "?"
		if list, err := xbl.ListFriends(ctx, s); err == nil {
			friends = fmt.Sprint(len(list))
			if len(list) >= friendLimit*9/10 {
				friends += " - nearly full, add another account: beacon accounts add <name>"
			}
		}
		fmt.Printf("%-12s %-22s %s (%s), friends %s/%d\n", a.name, a.tokenFile, gamertag, xuid, friends, friendLimit)
	}
	return nil
}

func accountsAdd(name string, log *slog.Logger) error {
	accounts, err := configuredAccounts()
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if strings.EqualFold(a.name, name) {
			return fmt.Errorf("there is already an account called %s", name)
		}
	}
	file := filepath.ToSlash(filepath.Join("tokens", name+".json"))
	if err := os.MkdirAll("tokens", 0o700); err != nil {
		return err
	}
	fmt.Printf("Sign in with the Xbox account that should host this world (not one already used here).\n")
	src, err := tokenSource(log, file, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gamertag, xuid, newSession, err := identityFrom(ctx, src)
	if err != nil {
		return fmt.Errorf("signed in, but could not read the account: %w", err)
	}
	for _, a := range accounts {
		if _, other, _, err := accountIdentity(ctx, a.tokenFile); err == nil && other == xuid {
			_ = os.Remove(file)
			return fmt.Errorf("%s is already used as account %q: sign in with a different Xbox account", gamertag, a.name)
		}
	}

	// The extra account and the main one friend each other, so players who friended one can find
	// the others' worlds through it.
	if _, mainXUID, mainSession, err := accountIdentity(ctx, tokenCacheFile); err == nil {
		_, err1 := xbl.SendFriendRequest(ctx, newSession, mainXUID)
		_, err2 := xbl.SendFriendRequest(ctx, mainSession, xuid)
		if err := errors.Join(err1, err2); err != nil {
			fmt.Println("Note: could not friend it with the main account:", err)
		}
	}

	block := fmt.Sprintf("\n# Added by beacon accounts add (%s).\n[[broadcast.extra]]\nname = %s\ntoken-file = %s\n"+
		"server = \"\"\nplayer-count = \"\"\n",
		gamertag, tomlQuote(name), tomlQuote(file))
	f, err := os.OpenFile(tomlConfigFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return err
	}
	fmt.Printf("Added %s as account %q (%s). Restart Beacon to start broadcasting from it.\n", gamertag, name, file)
	return nil
}

func accountsRemove(name string) error {
	if strings.EqualFold(name, "main") {
		return errors.New("the main account (token.json) can't be removed; sign in again by deleting token.json and restarting")
	}
	b, err := os.ReadFile(tomlConfigFile)
	if err != nil {
		return err
	}
	out, tokenFile, ok := removeExtraBlock(string(b), name)
	if !ok {
		return fmt.Errorf("no account called %s under [[broadcast.extra]]", name)
	}
	if err := os.WriteFile(tomlConfigFile, []byte(out), 0o644); err != nil {
		return err
	}
	// The token is kept, renamed, in case the account was removed by mistake.
	if tokenFile != "" {
		if err := os.Rename(tokenFile, tokenFile+".removed"); err == nil {
			tokenFile += ".removed"
		}
	}
	fmt.Printf("Removed account %s. Its sign-in is kept in %s. Restart Beacon to stop its broadcast.\n", name, tokenFile)
	return nil
}

// removeExtraBlock removes the [[broadcast.extra]] table named name from beacon.toml's text,
// along with the comment lines right above it, and returns its token-file.
func removeExtraBlock(text, name string) (string, string, bool) {
	lines := strings.Split(text, "\n")
	isHeader := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "[") }
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[broadcast.extra]]" {
			continue
		}
		end := i + 1
		for end < len(lines) && !isHeader(lines[end]) {
			end++
		}
		var gotName, tokenFile string
		for _, l := range lines[i+1 : end] {
			k, v, ok := strings.Cut(l, "=")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch strings.TrimSpace(k) {
			case "name":
				gotName = v
			case "token-file":
				tokenFile = v
			}
		}
		if gotName != name {
			continue
		}
		// Comments belonging to the next table stay with it.
		for end > i+1 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#") {
			end--
		}
		// The blank line before the table goes with it; the one after it stays.
		for end > i+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		start := i
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
			start--
		}
		if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
			start--
		}
		return strings.Join(append(lines[:start:start], lines[end:]...), "\n"), tokenFile, true
	}
	return text, "", false
}
