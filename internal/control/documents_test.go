package control

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestGrantDocumentsRequireStandaloneExplicitFlag(t *testing.T) {
	args := grantArguments()
	grant, ok := parseGrant(args)
	if !ok || grant.Documents {
		t.Fatal("text grant implicitly enabled documents")
	}
	grant, ok = parseGrant(append(append([]string(nil), args...), "--allow-documents"))
	if !ok || !grant.Documents {
		t.Fatal("explicit document grant rejected")
	}
	for _, suffix := range [][]string{{"--allow-documents", "--allow-documents"}, {"--allow-documents=true"}, {"--allow-documents", "true"}, {"--allow-documents", "false"}} {
		if _, ok := parseGrant(append(append([]string(nil), args...), suffix...)); ok {
			t.Fatal("ambiguous document flag accepted")
		}
	}
	control := &fakeTextController{}
	for _, enabled := range []bool{true, false} {
		command := append([]string{"grant"}, args...)
		if enabled {
			command = append(command, "--allow-documents")
		}
		var stdout, stderr bytes.Buffer
		if code := runTextCommand(context.Background(), command, &stdout, &stderr, control); code != 0 || control.saved.Documents != enabled {
			t.Fatalf("document permission did not reach controller: code=%d grant=%+v", code, control.saved)
		}
		var output bytes.Buffer
		if err := writeTextJSON(&output, newGrantRecord(control.saved)); err != nil {
			t.Fatal(err)
		}
		expected := `"documents":false`
		if enabled {
			expected = `"documents":true`
		}
		if !strings.Contains(output.String(), expected) {
			t.Fatal("grant JSON omitted document permission")
		}
	}
}
