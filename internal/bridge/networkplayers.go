package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sandertv/go-raknet"
)

// PlayerCount is the total of every server's own player count, as each one's server list entry or
// ping reports it: every player on the servers behind this relay, whichever way they came in
// (any Friends-tab world, an address, or another proxy such as Velocity in front of a server).
// A server that does not answer counts as 0; only when none answers is it an error.
//
// When the Java proxy keeps its count in PlayerCountDir (see onlinecount.go), the total is the one
// the server lists show instead: the relay's own Bedrock players plus that Java count. That one is
// exact even where a server's own answer would include players on another server (a Geyser on a
// proxy reports the whole proxy).
func (n *Network) PlayerCount(ctx context.Context) (int, error) {
	if javaCountKept() {
		return NetworkPlayers(), nil
	}
	servers := n.conf().servers
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var (
		mu    sync.Mutex
		total int
		errs  []error
		wg    sync.WaitGroup
	)
	for _, s := range servers {
		wg.Add(1)
		go func(s ServerConfig) {
			defer wg.Done()
			p, err := serverPlayers(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
				return
			}
			total += p
		}(s)
	}
	wg.Wait()
	if len(errs) == len(servers) && len(servers) > 0 {
		return 0, errors.Join(errs...)
	}
	return total, nil
}

// serverPlayers reads one server's own player count.
func serverPlayers(ctx context.Context, s ServerConfig) (int, error) {
	if strings.EqualFold(s.Transport, "raknet") {
		pong, err := raknet.PingContext(ctx, s.Address)
		if err != nil {
			return 0, err
		}
		return parsePongPlayers(pong)
	}
	entry, err := fetchServerListEntry(ctx, s.Address)
	if err != nil {
		return 0, err
	}
	return entry.Players, nil
}
