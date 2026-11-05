package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/go-logr/logr"
	"go.minekube.com/gate/pkg/edition/java/proxy"
	"go.minekube.com/gate/pkg/gate"
)

// Java Edition players come in through Gate (go.minekube.com/gate), a Velocity-compatible proxy
// written in Go, run inside this process. It reads its own config file - Gate's format, the
// counterpart of velocity.toml - and forwards players to the backends with Velocity's modern
// forwarding, which Dragonfly and Paper already accept. javaauth.go optionally adds premium/cracked
// Java logins (see javaAuthOptions).

// runJavaProxy runs Gate with the config file at path until ctx ends. dataDir holds the Java login
// files (premium-usernames.txt and premium_cache.json). authOpts, when not nil, turns on javaauth.go.
func runJavaProxy(ctx context.Context, path, dataDir string, authOpts *javaAuthOptions, log *slog.Logger) error {
	v := gate.NewViper()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read java proxy config %s: %w", path, err)
	}
	cfg, err := gate.LoadConfig(v)
	if err != nil {
		return fmt.Errorf("load java proxy config %s: %w", path, err)
	}
	jlog := log.With("src", "java")
	network := &javaNet{log: jlog}
	proxy.Plugins = append(proxy.Plugins,
		proxy.Plugin{Name: "beacon-network", Init: func(ctx context.Context, p *proxy.Proxy) error { return network.register(ctx, p) }},
	)
	if authOpts != nil {
		auth := newJavaAuth(filepath.Clean(dataDir), *authOpts, jlog)
		proxy.Plugins = append(proxy.Plugins,
			proxy.Plugin{Name: "beacon-javaauth", Init: func(_ context.Context, p *proxy.Proxy) error { return auth.register(p) }})
	}
	jlog.Info("java proxy starting", "config", path, "bind", cfg.Config.Bind)
	ctx = logr.NewContext(ctx, logr.FromSlogHandler(jlog.Handler()))
	return gate.Start(ctx, gate.WithConfig(*cfg), gate.WithAutoShutdownOnSignal(false))
}
