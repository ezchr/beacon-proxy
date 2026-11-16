package main

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// notInFirstRunFile are settings deliberately left out of the first-run beacon.toml (still in
// docs/config.md).
var notInFirstRunFile = map[string]bool{"listing-toggle": true}

// TestEveryConfigKeyDocumented keeps the first-run beacon.toml and docs/config.md complete: every
// setting Beacon reads must be in both, so neither falls behind the code again.
func TestEveryConfigKeyDocumented(t *testing.T) {
	keys := map[string]bool{}
	sections := map[string]bool{"broadcast": true, "advanced": true, "extra": true}
	var walk func(reflect.Type)
	walk = func(tp reflect.Type) {
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			tag := strings.Split(f.Tag.Get("toml"), ",")[0]
			if tag == "" || tag == "-" || tag == "servers" {
				continue
			}
			if sections[tag] {
				ft := f.Type
				for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
					ft = ft.Elem()
				}
				walk(ft)
				continue
			}
			keys[tag] = true
		}
	}
	walk(reflect.TypeOf(tomlConfig{}))
	// [servers] tables are read key by key in parseServers and the hold settings.
	re := regexp.MustCompile(`v\["([a-z-]+)"\]`)
	for _, f := range []string{"config_toml.go", "hold_config.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			keys[m[1]] = true
		}
	}
	doc, err := os.ReadFile("../../docs/config.md")
	if err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		word := regexp.MustCompile(`(^|[^a-z-])` + regexp.QuoteMeta(k) + `([^a-z-]|$)`)
		if !notInFirstRunFile[k] && !word.MatchString(defaultTOMLTemplate) {
			t.Errorf("%s is not in the first-run beacon.toml (default-beacon.toml)", k)
		}
		if !strings.Contains(string(doc), "`"+k+"`") && !strings.Contains(string(doc), "["+k+"]") {
			t.Errorf("%s is not in docs/config.md", k)
		}
	}
}
