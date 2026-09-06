package control

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestGrantVoiceNotesRequireStandaloneExplicitFlag(t *testing.T) {
	args := grantArguments()
	grant, ok := parseGrant(args)
	if !ok || grant.VoiceNotes {
		t.Fatal("text grant implicitly enabled voice_notes")
	}
	grant, ok = parseGrant(append(append([]string(nil), args...), "--allow-voice-notes"))
	if !ok || !grant.VoiceNotes {
		t.Fatal("explicit voice grant rejected")
	}
	for _, suffix := range [][]string{{"--allow-voice-notes", "--allow-voice-notes"}, {"--allow-voice-notes=true"}, {"--allow-voice-notes", "true"}, {"--allow-voice-notes", "false"}} {
		if _, ok := parseGrant(append(append([]string(nil), args...), suffix...)); ok {
			t.Fatal("ambiguous voice flag accepted")
		}
	}
	control := &fakeTextController{}
	for _, enabled := range []bool{true, false} {
		command := append([]string{"grant"}, args...)
		if enabled {
			command = append(command, "--allow-voice-notes")
		}
		var stdout, stderr bytes.Buffer
		if code := runTextCommand(context.Background(), command, &stdout, &stderr, control); code != 0 || control.saved.VoiceNotes != enabled {
			t.Fatalf("voice permission did not reach controller: code=%d grant=%+v", code, control.saved)
		}
		var output bytes.Buffer
		if err := writeTextJSON(&output, newGrantRecord(control.saved)); err != nil {
			t.Fatal(err)
		}
		expected := `"voice_notes":false`
		if enabled {
			expected = `"voice_notes":true`
		}
		if !strings.Contains(output.String(), expected) {
			t.Fatal("grant JSON omitted voice permission")
		}
	}
}
