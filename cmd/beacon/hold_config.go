package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/ezchr/beacon-proxy/internal/bridge"
)

// parseHold reads a server's hold, hold-message and hold-reasons (see the bridge's hold.go):
// hold = "90s" (or a number of seconds) keeps the server's players on Beacon while it restarts.
func parseHold(s *bridge.ServerConfig, v map[string]any) error {
	switch h := v["hold"].(type) {
	case nil:
	case string:
		h = strings.TrimSpace(h)
		if h == "" || h == "0" {
			break
		}
		d, err := time.ParseDuration(h)
		if err != nil || d < 0 {
			return fmt.Errorf(`hold %q: give a time such as "90s" or "2m"`, h)
		}
		s.Hold = d
	case int64:
		if h < 0 {
			return fmt.Errorf(`hold %d: give a time such as "90s" or "2m"`, h)
		}
		s.Hold = time.Duration(h) * time.Second
	case bool:
		if h {
			return fmt.Errorf(`hold = true: give a time such as "90s" or "2m"`)
		}
	default:
		return fmt.Errorf(`hold: give a time such as "90s" or "2m"`)
	}
	if s.Hold > bridge.MaxHold {
		return fmt.Errorf("hold %s: at most %s", s.Hold, bridge.MaxHold)
	}
	if s.Hold > 0 && s.Hold < 5*time.Second {
		return fmt.Errorf("hold %s: at least 5s (a restart takes longer)", s.Hold)
	}
	if m, ok := v["hold-message"]; ok {
		msg, ok := m.(string)
		if !ok {
			return fmt.Errorf(`hold-message must be text, e.g. hold-message = "Back in a moment..."`)
		}
		s.HoldMessage = strings.TrimSpace(msg)
	}
	if r, ok := v["hold-reasons"]; ok {
		list, ok := r.([]any)
		if !ok {
			return fmt.Errorf(`hold-reasons must be a list, e.g. hold-reasons = ["see you soon"]`)
		}
		for _, x := range list {
			if x, ok := x.(string); ok && strings.TrimSpace(x) != "" {
				s.HoldReasons = append(s.HoldReasons, strings.TrimSpace(x))
			}
		}
	}
	return nil
}
