package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ezchr/beacon-proxy/bridge"
	"github.com/robinbraemer/event"
	"go.minekube.com/common/minecraft/component"
	"go.minekube.com/gate/pkg/edition/java/proxy"
)

// The rest of what our Velocity plugins did for Java players, for the Java proxy (java.go):
//
//   - Datacenter networks are turned away, with the same lists, rules and joins.log as Bedrock joins
//     (bridge/dcblock.go), instead of Velocity's DcBlock plugin.
//   - The server list shows the whole network's player count: the relay's Bedrock players plus the
//     proxy's Java players (HybridAuth's NetworkCount).

// relayServers is the relay's server list, which names every Bedrock player it carries.
const relayServers = "http://127.0.0.1:19136/servers"

type javaNet struct {
	log     *slog.Logger
	proxy   *proxy.Proxy
	bedrock atomic.Int64
}

func (n *javaNet) register(ctx context.Context, p *proxy.Proxy) error {
	n.proxy = p
	event.Subscribe(p.Event(), 100, n.onPreLogin) // before javaAuth's premium check
	event.Subscribe(p.Event(), 0, n.onPing)
	go n.pollBedrock(ctx)
	return nil
}

func (n *javaNet) onPreLogin(e *proxy.PreLoginEvent) {
	conn := e.Conn()
	blocked, verdict, ip := bridge.DatacenterVerdict(conn.RemoteAddr())
	host := ""
	if vh := conn.VirtualHost(); vh != nil {
		host = vh.String()
	}
	bridge.LogJoin(fmt.Sprintf("%s java name=%s ip=%s version=%s host=%s %s (gate)",
		time.Now().UTC().Format(time.RFC3339Nano), e.Username(), ip, conn.Protocol(), host, verdict))
	if blocked {
		n.log.Warn("datacenter join turned away", "name", e.Username(), "ip", ip, "verdict", verdict)
		e.Deny(&component.Text{Content: bridge.DatacenterMessage})
	}
}

func (n *javaNet) onPing(e *proxy.PingEvent) {
	ping := e.Ping()
	if ping == nil || ping.Players == nil {
		return
	}
	ping.Players.Online = int(n.bedrock.Load()) + n.proxy.PlayerCount()
}

// pollBedrock reads the relay's Bedrock player count every few seconds. On failure the last count
// stays.
func (n *javaNet) pollBedrock(ctx context.Context) {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		if resp, err := client.Get(relayServers); err == nil {
			var body struct {
				Players map[string]json.RawMessage `json:"players"`
			}
			if json.NewDecoder(resp.Body).Decode(&body) == nil {
				n.bedrock.Store(int64(len(body.Players)))
			}
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
