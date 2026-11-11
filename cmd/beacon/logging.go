package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// consoleHandler writes log lines the way server consoles do:
//
//	[2026-11-10 08:28:23 INFO] network mode: players can move between servers servers=[pvp skygen]
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Leveler
	attrs string // pre-formatted attributes from With
	group string
}

func newConsoleHandler(w io.Writer, level slog.Leveler) *consoleHandler {
	return &consoleHandler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	level := r.Level.String()
	if r.Level == slog.LevelWarn {
		level = "WARN"
	}
	fmt.Fprintf(&b, "[%s %s] %s", t.Format("2006-01-02 15:04:05"), level, r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, h.group, a)
		return true
	})
	b.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	for _, a := range attrs {
		writeAttr(&b, h.group, a)
	}
	c := *h
	c.attrs += b.String()
	return &c
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	c := *h
	if c.group != "" {
		name = c.group + "." + name
	}
	c.group = name
	return &c
}

func writeAttr(b *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, g := range a.Value.Group() {
			writeAttr(b, key, g)
		}
		return
	}
	v := a.Value.String()
	if v == "" || strings.ContainsAny(v, " \t\n\"=") {
		v = fmt.Sprintf("%q", v)
	}
	fmt.Fprintf(b, " %s=%s", key, v)
}
