package bridge

import (
	"path"
	"strings"
	"sync/atomic"
)

// protectedNames are name patterns staff can't ban, mute or kick (beacon.toml protected), such as a
// server's own bots: RiftBot* matches RiftBot1, RiftBot2 and so on. Case doesn't matter.
var protectedNames atomic.Pointer[[]string]

// SetProtected sets the protected name patterns.
func SetProtected(patterns []string) {
	var list []string
	for _, p := range patterns {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			list = append(list, p)
		}
	}
	protectedNames.Store(&list)
}

// isProtected reports whether name matches a protected pattern.
func isProtected(name string) bool {
	list := protectedNames.Load()
	if list == nil || name == "" {
		return false
	}
	name = strings.ToLower(strings.TrimPrefix(name, "java:"))
	for _, p := range *list {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

const protectedMessage = "<red>%s is protected (a server bot): it can't be banned, muted or kicked.</red>"
