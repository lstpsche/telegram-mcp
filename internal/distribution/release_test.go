package distribution

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

func releaseFixture(t *testing.T) (*Installer, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "private")
	if err := privatefs.EnsureDirectory(root); err != nil {
		t.Fatal(err)
	}
	i := &Installer{Root: root, Platform: runtime.GOOS + "-" + runtime.GOARCH}
	return i, fixtureArchive(t, i.Platform, nil)
}
func fixtureArchive(t *testing.T, platform string, mutate func(map[string][]byte)) []byte {
	t.Helper()
	files := map[string][]byte{"README.md": []byte("synthetic docs")}
	for _, name := range []string{"telegram-mcp", "telegram-mcpd", "telegram-mcpctl"} {
		if strings.HasPrefix(platform, "windows-") {
			name += ".exe"
		}
		files[name] = []byte("synthetic executable")
	}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		digest := sha256.Sum256(files[name])
		fmt.Fprintf(&sums, "%x  %s\n", digest, name)
	}
	files["SHA256SUMS"] = []byte(sums.String())
	if mutate != nil {
		mutate(files)
	}
	var b bytes.Buffer
	archive := zip.NewWriter(&b)
	for name, data := range files {
		header := &zip.FileHeader{Name: "telegram-mcp-0.2.0-" + platform + "/" + name, Method: zip.Deflate}
		header.SetMode(0600)
		w, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestInstallVerifiesAndResumesWithoutReplacement(t *testing.T) {
	i, archive := releaseFixture(t)
	directory, err := i.installArchive(context.Background(), "0.2.0", archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := privatefs.CheckDirectory(directory); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, Binary("telegram-mcpctl"))
	before, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	again, err := i.installArchive(context.Background(), "0.2.0", archive)
	if err != nil || again != directory {
		t.Fatal(err)
	}
	after, err := os.Stat(executable)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replaced existing executable")
	}
	if err := os.WriteFile(executable, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := i.installArchive(context.Background(), "0.2.0", archive); err == nil {
		t.Fatal("accepted changed executable")
	}
}
func TestUnsafeArchivesNeverPublish(t *testing.T) {
	for name, mutate := range map[string]func(map[string][]byte){
		"traversal":         func(f map[string][]byte) { f["../escape"] = []byte("bad") },
		"backslash":         func(f map[string][]byte) { f[`docs\escape`] = []byte("bad") },
		"case collision":    func(f map[string][]byte) { f["readme.md"] = []byte("bad") },
		"checksum mismatch": func(f map[string][]byte) { f["README.md"] = []byte("bad") },
		"missing binary":    func(f map[string][]byte) { delete(f, Binary("telegram-mcpctl")) },
		"missing sums":      func(f map[string][]byte) { delete(f, "SHA256SUMS") },
		"unlisted file":     func(f map[string][]byte) { f["extra"] = []byte("bad") },
	} {
		t.Run(name, func(t *testing.T) {
			i, _ := releaseFixture(t)
			archive := fixtureArchive(t, i.Platform, mutate)
			if _, err := i.installArchive(context.Background(), "0.2.0", archive); err == nil {
				t.Fatal("accepted unsafe archive")
			}
			if _, err := os.Stat(filepath.Join(i.Root, "versions", "0.2.0")); !os.IsNotExist(err) {
				t.Fatal("published failed archive")
			}
		})
	}
}
func TestManifestIdentityAndStrictShape(t *testing.T) {
	i, archive := releaseFixture(t)
	digest := sha256.Sum256(archive)
	m := Manifest{Version: "0.2.0", Commit: strings.Repeat("a", 40), Qualification: "unsigned-build-only", Artifacts: []Artifact{{Platform: i.Platform, Artifact: "telegram-mcp-0.2.0-" + i.Platform + ".zip", SHA256: hex.EncodeToString(digest[:])}}}
	data, _ := json.Marshal(m)
	if _, _, err := parseManifest(data, "0.2.0", i.Platform); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{append([]byte(`{"version":"0.2.0",`), data[1:]...), bytes.Replace(data, []byte("unsigned-build-only"), []byte("development-build-only"), 1), bytes.Replace(data, []byte(`"artifacts"`), []byte(`"Artifacts"`), 1)} {
		if _, _, err := parseManifest(invalid, "0.2.0", i.Platform); err == nil {
			t.Fatal("accepted invalid manifest")
		}
	}
	if _, _, err := parseManifest(data, "0.3.0", i.Platform); err == nil {
		t.Fatal("accepted wrong version")
	}
}
func TestDownloadChecksArchiveDigestAndCancellation(t *testing.T) {
	i, archive := releaseFixture(t)
	digest := sha256.Sum256(archive)
	manifest := Manifest{Version: "0.2.0", Commit: strings.Repeat("a", 40), Qualification: "unsigned-build-only", Artifacts: []Artifact{{Platform: i.Platform, Artifact: "telegram-mcp-0.2.0-" + i.Platform + ".zip", SHA256: hex.EncodeToString(digest[:])}}}
	bad := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "release.json") {
			json.NewEncoder(w).Encode(manifest)
			return
		}
		if bad {
			w.Write([]byte("wrong archive"))
			return
		}
		w.Write(archive)
	}))
	defer server.Close()
	i.BaseURL = server.URL + "/"
	i.Client = server.Client()
	if _, err := i.Install(context.Background(), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	bad = true
	if _, err := i.Install(context.Background(), "0.2.0"); err == nil {
		t.Fatal("accepted mismatched archive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := i.Install(ctx, "0.2.0"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestArchiveRejectsLinksAndDuplicateEntries(t *testing.T) {
	for _, mode := range []os.FileMode{os.ModeSymlink | 0700, 0600} {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for n := 0; n < 4; n++ {
			h := &zip.FileHeader{Name: "telegram-mcp-0.2.0-linux-amd64/README.md"}
			h.SetMode(mode)
			f, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte("target"))
		}
		w.Close()
		if _, _, err := inspectArchive(b.Bytes(), "telegram-mcp-0.2.0-linux-amd64", "linux-amd64"); err == nil {
			t.Fatal("accepted duplicate or link")
		}
	}
}
