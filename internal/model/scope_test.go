package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestScopeReferencesAndNames(t *testing.T) {
	valid := "tgscope:v1:0123456789abcdef0123456789abcdef"
	id, err := ParseScopeID(valid)
	if err != nil || id.String() != valid {
		t.Fatal("valid reference rejected", err)
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ScopeID
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != id {
		t.Fatal("reference round trip failed", err)
	}
	for _, invalid := range []string{"", strings.ToUpper(valid), valid + "a", valid[:len(valid)-1], strings.Replace(valid, "v1", "v2", 1), " " + valid, valid + "\n"} {
		if _, err := ParseScopeID(invalid); !errors.Is(err, ErrInvalidReference) {
			t.Fatal("invalid reference accepted")
		}
		if ScopeID(invalid).String() != "" {
			t.Fatal("invalid value has a reference string")
		}
		if _, err := json.Marshal(ScopeID(invalid)); err == nil {
			t.Fatal("invalid value serialized")
		}
	}
	for _, invalid := range []string{`null`, `42`, `{}`, `[]`, `""`, `"tgscope:v1:secret"`} {
		decoded = id
		if err := json.Unmarshal([]byte(invalid), &decoded); err == nil || decoded != id {
			t.Fatal("invalid JSON accepted or changed destination")
		}
	}
	for _, name := range []string{"a", "work", "team_1-2", strings.Repeat("a", 32)} {
		if !ValidScopeName(name) {
			t.Fatal("valid name rejected")
		}
	}
	for _, name := range []string{"", "A", "1team", "-team", "two words", "work\n", "équipe", strings.Repeat("a", 33)} {
		if ValidScopeName(name) {
			t.Fatal("invalid name accepted")
		}
	}
}
