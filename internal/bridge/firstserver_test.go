package bridge

import "testing"

func TestFirstServerFirst(t *testing.T) {
	order := []ServerConfig{{Name: "pvp"}, {Name: "skygen"}, {Name: "lobby"}}
	names := func(o []ServerConfig) string {
		s := ""
		for _, c := range o {
			s += c.Name + " "
		}
		return s
	}
	for _, c := range []struct{ first, want string }{
		{"", "pvp skygen lobby "},
		{"Skygen", "skygen pvp lobby "},
		{"lobby", "lobby pvp skygen "},
		{"nope", "pvp skygen lobby "},
	} {
		if got := names(firstServerFirst(order, c.first)); got != c.want {
			t.Errorf("first %q: got %q, want %q", c.first, got, c.want)
		}
	}
	if names(order) != "pvp skygen lobby " {
		t.Errorf("order was changed in place: %q", names(order))
	}
}
