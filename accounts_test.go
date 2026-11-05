package main

import "testing"

func TestRemoveExtraBlock(t *testing.T) {
	in := `[advanced]
control-port = 7777

# Added by beacon accounts add (Alt1).
[[broadcast.extra]]
name = "alt1"
token-file = "tokens/alt1.json"

# Added by beacon accounts add (Alt2).
[[broadcast.extra]]
name = "alt2"
token-file = "tokens/alt2.json"
`
	out, file, ok := removeExtraBlock(in, "alt1")
	want := `[advanced]
control-port = 7777

# Added by beacon accounts add (Alt2).
[[broadcast.extra]]
name = "alt2"
token-file = "tokens/alt2.json"
`
	if !ok || file != "tokens/alt1.json" || out != want {
		t.Fatalf("ok %v file %q\n%s", ok, file, out)
	}
	if _, _, ok := removeExtraBlock(in, "nope"); ok {
		t.Fatal("removed an account that is not there")
	}
}
