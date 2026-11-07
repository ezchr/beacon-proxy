package bridge

import (
	"testing"
	"time"
)

func TestCombat(t *testing.T) {
	SetCombat([]string{"123", "ABC-def"}, time.Second)
	if CombatLeft("123") <= 0 || CombatLeft("abc-DEF") <= 0 {
		t.Fatal("tagged players must be in combat, whatever the case of a UUID")
	}
	if CombatLeft("456") != 0 {
		t.Fatal("others must not be")
	}
	SetCombat([]string{"789"}, -time.Second)
	if CombatLeft("789") != 0 {
		t.Fatal("an ended tag must not count")
	}
}
