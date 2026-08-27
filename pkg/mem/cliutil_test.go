package mem

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseGlobalFlagStrictRejectsInvalidScope(t *testing.T) {
	tests := [][]string{
		{"--dir"},
		{"--dir="},
		{"--dir", "--global"},
		{"--dir", "one", "--dir=two"},
		{"--global", "--dir", "one"},
	}
	for _, args := range tests {
		if _, _, _, err := ParseGlobalFlagStrict(args); err == nil {
			t.Fatalf("ParseGlobalFlagStrict(%q) accepted invalid scope", args)
		}
	}
}

func TestParseGlobalFlagStrictKeepsCommandArguments(t *testing.T) {
	global, dir, remaining, err := ParseGlobalFlagStrict([]string{"recent", "--dir", "C:/notes", "-limit", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if global || dir != "C:/notes" || !reflect.DeepEqual(remaining, []string{"recent", "-limit", "5"}) {
		t.Fatalf("unexpected parse: global=%v dir=%q remaining=%q", global, dir, remaining)
	}
}

func TestStrictGlobalFlagsHonorArgumentTerminator(t *testing.T) {
	_, _, remaining, err := ParseGlobalFlagStrict([]string{"add", "--", "--dir"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remaining, []string{"add", "--", "--dir"}) {
		t.Fatalf("argument terminator was not preserved: %q", remaining)
	}
}

func TestParseColorFlagStrictAcceptsAutoAndRejectsUnknownMode(t *testing.T) {
	mode, remaining, err := ParseColorFlagStrict([]string{"recent", "--color=auto", "-limit", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "auto" || !reflect.DeepEqual(remaining, []string{"recent", "-limit", "5"}) {
		t.Fatalf("unexpected parse: mode=%q remaining=%q", mode, remaining)
	}
	if _, _, err := ParseColorFlagStrict([]string{"--color=rainbow"}); err == nil || !strings.Contains(err.Error(), "режим") {
		t.Fatalf("unknown color mode error = %v", err)
	}
}
