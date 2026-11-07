package scraper

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Players seen by address expire after 30 days; players found in Friends-tab worlds are kept.
func TestPruneOnlyServerPlayers(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()
	for _, name := range []string{serverPlayersFile, worldPlayersFile} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("111,Old,somewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seenPath(p), []byte(fmt.Sprintf("111,%d\n", old)), 0o644); err != nil {
			t.Fatal(err)
		}
		pruneExpired(p, slog.Default())
	}
	if lines, _ := readLines(filepath.Join(dir, serverPlayersFile)); len(lines) != 0 {
		t.Errorf("server list kept an expired player: %v", lines)
	}
	if lines, _ := readLines(filepath.Join(dir, worldPlayersFile)); len(lines) != 1 {
		t.Errorf("world list lost a player: %v", lines)
	}
}
