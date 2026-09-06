// Package distribution installs verified release files without opening account state.
package distribution

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/privatefs"
)

const ReleaseURL = "https://github.com/lstpsche/telegram-mcp/releases/download/"
const maxArchive = 256 << 20
const maxFile = 128 << 20
const maxPayload = 512 << 20

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var platformPattern = regexp.MustCompile(`^(darwin|linux|windows)-(amd64|arm64)$`)
var filenamePattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

func ValidVersion(version string) bool {
	return len(version) <= 64 && versionPattern.MatchString(version)
}
func Binary(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
func DefaultRoot() (string, error) {
	p, err := daemon.DefaultPaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.StateDir, "install"), nil
}

type Artifact struct {
	Platform string `json:"platform"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}
type Manifest struct {
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	Qualification string     `json:"qualification"`
	Artifacts     []Artifact `json:"artifacts"`
}

func parseManifest(data []byte, version, platform string) (Manifest, Artifact, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, Artifact{}, errors.New("invalid release manifest")
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return m, Artifact{}, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return m, Artifact{}, errors.New("release manifest is not canonical")
	}
	if !ValidVersion(version) || m.Version != version || !commitPattern.MatchString(m.Commit) || m.Qualification != "unsigned-build-only" || len(m.Artifacts) < 1 || len(m.Artifacts) > 6 {
		return m, Artifact{}, errors.New("release identity or qualification mismatch")
	}
	seen := map[string]bool{}
	var selected Artifact
	for _, a := range m.Artifacts {
		if !platformPattern.MatchString(a.Platform) || seen[a.Platform] || a.Artifact != "telegram-mcp-"+version+"-"+a.Platform+".zip" || !digestPattern.MatchString(a.SHA256) {
			return m, Artifact{}, errors.New("invalid release artifact")
		}
		seen[a.Platform] = true
		if a.Platform == platform {
			selected = a
		}
	}
	if selected.Platform == "" {
		return m, Artifact{}, errors.New("release does not support this platform")
	}
	return m, selected, nil
}

// Installer's HTTP client is the external download seam. Root holds immutable versions.
type Installer struct {
	Root     string
	Client   *http.Client
	BaseURL  string
	Platform string
}

func Default() (*Installer, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	return &Installer{Root: root, Client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe release redirect")
		}
		return nil
	}}, BaseURL: ReleaseURL, Platform: runtime.GOOS + "-" + runtime.GOARCH}, nil
}

func (i *Installer) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := i.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, errors.New("release download rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read release download: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release download exceeds limit")
	}
	return data, nil
}

// Install downloads and validates before publishing a complete version directory.
// An identical existing version is verified in full, never overwritten.
func (i *Installer) Install(ctx context.Context, version string) (directory string, result error) {
	if !ValidVersion(version) || !platformPattern.MatchString(i.Platform) || i.Client == nil {
		return "", errors.New("invalid release selection")
	}
	if order, err := CompareVersions(version, "0.2.0"); err != nil || order < 0 {
		return "", errors.New("managed installation requires version 0.2.0 or later")
	}
	if err := privatefs.EnsureDirectory(i.Root); err != nil {
		return "", err
	}
	lock, err := daemon.AcquireAccountLock(filepath.Join(i.Root, "install.lock"))
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	base := i.BaseURL + "v" + version + "/"
	data, err := i.fetch(ctx, base+"release.json", 64<<10)
	if err != nil {
		return "", err
	}
	_, artifact, err := parseManifest(data, version, i.Platform)
	if err != nil {
		return "", err
	}
	archive, err := i.fetch(ctx, base+artifact.Artifact, maxArchive)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return "", errors.New("release archive checksum mismatch")
	}
	directory, err = i.installArchive(ctx, version, archive)
	if err != nil {
		return "", err
	}
	if err := prepareEntries(i.Root, directory); err != nil {
		return "", err
	}
	return directory, nil
}

func (i *Installer) installArchive(ctx context.Context, version string, data []byte) (directory string, result error) {
	files, checksums, err := inspectArchive(data, "telegram-mcp-"+version+"-"+i.Platform, i.Platform)
	if err != nil {
		return "", err
	}
	versions := filepath.Join(i.Root, "versions")
	if err := privatefs.EnsureDirectory(versions); err != nil {
		return "", err
	}
	target := filepath.Join(versions, version)
	if _, err := os.Lstat(target); err == nil {
		if err := verifyExisting(ctx, target, files, checksums); err != nil {
			return "", err
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// MkdirTemp supplies an unpredictable exclusive name. Privatefs creates the
	// child with native ACLs before any release bytes are written.
	staging, err := os.MkdirTemp(versions, ".download-")
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(staging)) }()
	payload := filepath.Join(staging, "payload")
	if err := privatefs.EnsureDirectory(payload); err != nil {
		return "", err
	}
	for name, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		body, err := readZip(file, maxFile)
		if err != nil {
			return "", err
		}
		if name != "SHA256SUMS" && !matchesDigest(body, checksums[name]) {
			return "", errors.New("release payload checksum mismatch")
		}
		destination := filepath.Join(payload, filepath.FromSlash(name))
		if err := privatefs.EnsureDirectory(filepath.Dir(destination)); err != nil {
			return "", err
		}
		if isBinary(name, i.Platform) {
			err = privatefs.WriteExecutable(destination, body)
		} else {
			err = privatefs.WriteFile(destination, body, false)
		}
		if err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(payload, target); err != nil {
		return "", err
	}
	return target, nil
}

func inspectArchive(data []byte, prefix, platform string) (map[string]*zip.File, map[string]string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, errors.New("invalid release ZIP")
	}
	if len(archive.File) < 4 || len(archive.File) > 256 {
		return nil, nil, errors.New("invalid release file count")
	}
	files := map[string]*zip.File{}
	folded := map[string]bool{}
	var total uint64
	for _, file := range archive.File {
		name, ok := strings.CutPrefix(file.Name, prefix+"/")
		if !ok || !validName(name) || !file.Mode().IsRegular() || file.UncompressedSize64 > maxFile || folded[strings.ToLower(name)] {
			return nil, nil, errors.New("unsafe release ZIP entry")
		}
		total += file.UncompressedSize64
		if total > maxPayload {
			return nil, nil, errors.New("release payload exceeds limit")
		}
		files[name] = file
		folded[strings.ToLower(name)] = true
	}
	sums, ok := files["SHA256SUMS"]
	if !ok {
		return nil, nil, errors.New("release payload checksums missing")
	}
	data, err = readZip(sums, 64<<10)
	if err != nil {
		return nil, nil, err
	}
	checksums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok || !digestPattern.MatchString(digest) || !validName(name) || name == "SHA256SUMS" || checksums[name] != "" || files[name] == nil {
			return nil, nil, errors.New("invalid payload checksum list")
		}
		checksums[name] = digest
	}
	if len(checksums) != len(files)-1 {
		return nil, nil, errors.New("incomplete payload checksum list")
	}
	for _, name := range []string{"telegram-mcp", "telegram-mcpd", "telegram-mcpctl"} {
		if strings.HasPrefix(platform, "windows-") {
			name += ".exe"
		}
		if files[name] == nil {
			return nil, nil, errors.New("release executable missing")
		}
	}
	return files, checksums, nil
}
func validName(name string) bool {
	if name == "" || !filenamePattern.MatchString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}
func readZip(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("release file exceeds limit")
	}
	input, err := file.Open()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(input, limit+1))
	err = errors.Join(readErr, input.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release file exceeds limit")
	}
	return data, nil
}
func matchesDigest(data []byte, digest string) bool {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == digest
}
func isBinary(name, platform string) bool {
	for _, binary := range []string{"telegram-mcp", "telegram-mcpd", "telegram-mcpctl"} {
		if strings.HasPrefix(platform, "windows-") {
			binary += ".exe"
		}
		if name == binary {
			return true
		}
	}
	return false
}
func verifyExisting(ctx context.Context, target string, files map[string]*zip.File, checksums map[string]string) error {
	if err := privatefs.CheckDirectory(target); err != nil {
		return err
	}
	count := 0
	err := filepath.WalkDir(target, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return privatefs.CheckDirectory(filename)
		}
		name, err := filepath.Rel(target, filename)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		file := files[name]
		if file == nil {
			return errors.New("unexpected installed file")
		}
		var body []byte
		if isBinary(name, runtime.GOOS+"-"+runtime.GOARCH) {
			body, err = privatefs.ReadExecutable(filename, maxFile)
		} else {
			body, err = privatefs.ReadFile(filename, maxFile)
		}
		if err != nil {
			return err
		}
		if name == "SHA256SUMS" {
			expected, err := readZip(file, 64<<10)
			if err != nil {
				return err
			}
			if !bytes.Equal(body, expected) {
				return errors.New("installed checksums changed")
			}
		} else if !matchesDigest(body, checksums[name]) {
			return errors.New("installed payload changed")
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(files) {
		return errors.New("installed payload incomplete")
	}
	return nil
}
