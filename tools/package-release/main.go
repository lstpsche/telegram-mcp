// package-release creates portable archives without accessing account state.
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type options struct{ Version, Output, Targets, Mode string }
type commandRunner func(context.Context, []string, string, ...string) ([]byte, error)

func main() {
	var o options
	flag.StringVar(&o.Version, "version", "", "release version, for example 1.0.0")
	flag.StringVar(&o.Output, "output-dir", "", "new absolute output directory")
	flag.StringVar(&o.Targets, "targets", "darwin/arm64,darwin/amd64,linux/arm64,linux/amd64,windows/arm64,windows/amd64", "comma-separated OS/architecture targets")
	flag.StringVar(&o.Mode, "mode", "distribution", "distribution (clean source) or development (allows dirty source)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "package-release: unexpected positional arguments")
		os.Exit(2)
	}
	if err := buildRelease(ctx, o, runCommand); err != nil {
		fmt.Fprintln(os.Stderr, "package-release:", err)
		os.Exit(1)
	}
	fmt.Println("Archives created; release.json records hashes and source identity. Native execution, installation and publication were not performed.")
}

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 5 * time.Second
	command.Env = append(os.Environ(), env...)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(exitError.Stderr)))
		}
		return nil, fmt.Errorf("%s failed: %w", filepath.Base(name), err)
	}
	return output, nil
}

func (o options) validate() error {
	if len(o.Version) > 64 || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(o.Version) {
		return errors.New("invalid release version")
	}
	if !filepath.IsAbs(o.Output) || filepath.Clean(o.Output) != o.Output {
		return errors.New("output must be a new absolute canonical directory")
	}
	if o.Mode != "development" && o.Mode != "distribution" {
		return errors.New("select development or distribution mode")
	}
	seen := map[string]bool{}
	for _, target := range strings.Split(o.Targets, ",") {
		if !regexp.MustCompile(`^(darwin|linux|windows)/(amd64|arm64)$`).MatchString(target) || seen[target] {
			return errors.New("targets must be unique supported OS/architecture pairs")
		}
		seen[target] = true
	}
	return nil
}

type releaseArtifact struct {
	Platform string `json:"platform"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}
type releaseManifest struct {
	Version       string            `json:"version"`
	Commit        string            `json:"commit"`
	Qualification string            `json:"qualification"`
	Artifacts     []releaseArtifact `json:"artifacts"`
}

func buildRelease(ctx context.Context, o options, run commandRunner) error {
	if err := o.validate(); err != nil {
		return err
	}
	buildEnv := []string{"GO111MODULE=on", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=", "GOAMD64=v1", "GOARM64=v8.0"}
	version, err := run(ctx, buildEnv, "go", "version")
	if err != nil {
		return err
	}
	if !strings.Contains(string(version), "go1.27.1 ") {
		return errors.New("Go 1.27.1 is required")
	}
	head, err := run(ctx, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(string(head))
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) {
		return errors.New("invalid source revision")
	}
	dirty, err := run(ctx, nil, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		if o.Mode == "distribution" {
			return errors.New("distribution requires a clean checkout")
		}
		commit += "-dirty"
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(o.Output))
	if err != nil {
		return errors.New("output parent must already exist")
	}
	if parent != filepath.Dir(o.Output) {
		return errors.New("output parent must be canonical without symlinks")
	}
	docs, err := filepath.Abs("docs")
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.VolumeName(docs), filepath.VolumeName(o.Output)) {
		relative, err := filepath.Rel(docs, o.Output)
		if err != nil {
			return err
		}
		if filepath.IsLocal(relative) {
			return errors.New("output must be outside the documentation source")
		}
	}
	if err := os.Mkdir(o.Output, 0700); err != nil {
		return fmt.Errorf("create fresh output directory: %w", err)
	}
	manifest := releaseManifest{Version: o.Version, Commit: commit, Qualification: "unsigned-build-only"}
	if o.Mode == "development" {
		manifest.Qualification = "development-build-only"
	}
	for _, target := range strings.Split(o.Targets, ",") {
		parts := strings.Split(target, "/")
		platform := strings.ReplaceAll(target, "/", "-")
		name := "telegram-mcp-" + o.Version + "-" + platform
		payload := filepath.Join(o.Output, name)
		if err := os.Mkdir(payload, 0700); err != nil {
			return err
		}
		env := append(append([]string{}, buildEnv...), "GOOS="+parts[0], "GOARCH="+parts[1])
		for _, binary := range []string{"telegram-mcp", "telegram-mcpctl", "telegram-mcpd"} {
			filename := binary
			if parts[0] == "windows" {
				filename += ".exe"
			}
			ldflags := "-X github.com/lstpsche/telegram-mcp/internal/buildinfo.Version=" + o.Version + " -X github.com/lstpsche/telegram-mcp/internal/buildinfo.Commit=" + commit
			if _, err := run(ctx, env, "go", "build", "-trimpath", "-ldflags", ldflags, "-o", filepath.Join(payload, filename), "./cmd/"+binary); err != nil {
				return err
			}
		}
		if err := copyReleaseDocs(payload); err != nil {
			return err
		}
		if err := writePayloadChecksums(payload); err != nil {
			return err
		}
		artifact := name + ".zip"
		if err := archivePayload(payload, filepath.Join(o.Output, artifact)); err != nil {
			return err
		}
		digest, err := fileDigest(filepath.Join(o.Output, artifact))
		if err != nil {
			return err
		}
		manifest.Artifacts = append(manifest.Artifacts, releaseArtifact{Platform: platform, Artifact: artifact, SHA256: digest})
	}
	if o.Mode == "distribution" {
		current, err := run(ctx, nil, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		changed, err := run(ctx, nil, "git", "status", "--porcelain")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(current)) != commit || len(changed) > 0 {
			return errors.New("source changed while building distribution")
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.Output, "release.json"), append(data, '\n'), 0600)
}

func archivePayload(payload, path string) (resultError error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { resultError = errors.Join(resultError, file.Close()) }()
	archive := zip.NewWriter(file)
	defer func() { resultError = errors.Join(resultError, archive.Close()) }()
	return filepath.WalkDir(payload, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("release payload contains a non-regular file")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(payload), path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		// A Windows build host cannot represent Unix executable mode on disk.
		if filepath.Dir(path) == payload {
			switch entry.Name() {
			case "telegram-mcp", "telegram-mcpctl", "telegram-mcpd":
				header.SetMode(0700)
			}
		}
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyError := io.Copy(writer, input)
		return errors.Join(copyError, input.Close())
	})
}

func copyReleaseDocs(payload string) error {
	if err := filepath.WalkDir("docs", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(payload, path)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("release documentation contains a non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	}); err != nil {
		return fmt.Errorf("copy release documentation: %w", err)
	}
	data, err := os.ReadFile("README.md")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(payload, "README.md"), data, 0600)
}

func fileDigest(path string) (digest string, resultError error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { resultError = errors.Join(resultError, f.Close()) }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writePayloadChecksums(payload string) error {
	var lines strings.Builder
	err := filepath.WalkDir(payload, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("release payload contains non-regular file")
		}
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(payload, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&lines, "%s  %s\n", digest, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(payload, "SHA256SUMS"), []byte(lines.String()), 0600)
}
