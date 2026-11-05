// Package worlds (`beacon worlds`) lists the Friends-tab worlds an account's friends have open right now, busiest
// first. Read-only: it joins nothing, and never writes the token file it is given (a refreshed
// token is not saved), so it is safe to point at a token a running broadcaster also uses.
//
// Usage: beacon worlds [token file]   (default tokens/scraper.json)
package worlds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/ezchr/beacon-proxy/internal/session"
	"github.com/ezchr/beacon-proxy/internal/xbl"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"golang.org/x/oauth2"
)

// Main runs `beacon worlds [token file]`; the default is the scraper's account.
func Main(args []string) {
	file := "tokens/scraper.json"
	if len(args) > 0 {
		file = args[0]
	}
	b, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}
	tok := new(oauth2.Token)
	if err := json.Unmarshal(b, tok); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s, err := session.SessionFromTokenSource(auth.AndroidConfig.RefreshTokenSource(tok), auth.AndroidConfig, ctx)
	if err != nil {
		panic(err)
	}
	friends, err := xbl.ListFriends(ctx, s)
	if err != nil {
		panic(err)
	}
	xuids := make([]string, 0, len(friends))
	for _, f := range friends {
		xuids = append(xuids, f.XUID)
	}
	var worlds []xbl.ActiveSession
	for i := 0; i < len(xuids); i += 100 {
		part, err := xbl.ActivitiesForXUIDs(ctx, s, xuids[i:min(i+100, len(xuids))])
		if err != nil {
			panic(err)
		}
		worlds = append(worlds, part...)
	}
	sort.Slice(worlds, func(i, j int) bool { return worlds[i].Custom.MemberCount > worlds[j].Custom.MemberCount })
	fmt.Printf("%d friends, %d open worlds\n", len(friends), len(worlds))
	isFriend := make(map[string]bool, len(friends))
	for _, f := range friends {
		isFriend[f.XUID] = true
	}
	for _, w := range worlds {
		via := "hosted by a friend"
		if w.OwnerXUID != w.Custom.OwnerId {
			via = "a friend is playing in it (" + w.OwnerXUID + ")"
		}
		if !isFriend[w.Custom.OwnerId] {
			via += ", host NOT a friend"
		}
		fmt.Printf("%2d/%-2d  %-32q host %-18s protocol %-4d %-9s %s\n", w.Custom.MemberCount, w.Custom.MaxMemberCount, w.Custom.WorldName, w.Custom.HostName, w.Custom.Protocol, w.Custom.Version, via)
	}
}
