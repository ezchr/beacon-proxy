package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The admins and moderators lists get each XUID's gamertag as a comment, one XUID a line, and the
// rest of beacon.toml is left as it was.
func TestAnnotateStaffNames(t *testing.T) {
	p := filepath.Join(t.TempDir(), "beacon.toml")
	in := "# top\n[broadcast]\nadmins = [\"111\", \"222\"] # old\nmoderators = [\n  \"333\", # Kept\n]\nother = [\"x\"]\n"
	if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	annotateStaffNames(p, "222", "NewName")
	out, _ := os.ReadFile(p)
	got := string(out)
	for _, want := range []string{
		"# top\n[broadcast]\n",
		"admins = [\n  \"111\",\n  \"222\", # NewName\n]",
		"moderators = [\n  \"333\", # Kept\n]",
		"other = [\"x\"]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	annotateStaffNames(p, "222", "Renamed")
	out, _ = os.ReadFile(p)
	if !strings.Contains(string(out), "\"222\", # Renamed") || strings.Contains(string(out), "NewName") {
		t.Errorf("rename not kept up to date:\n%s", out)
	}
}
