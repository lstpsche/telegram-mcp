package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type packageFake struct {
	calls  []string
	failAt int
	dirty  bool
	commit string
}

func (f *packageFake) run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " ")+" "+strings.Join(env, " "))
	if f.failAt == len(f.calls) {
		return nil, errors.New("synthetic failure")
	}
	switch {
	case name == "git" && args[0] == "rev-parse":
		return []byte(f.commit), nil
	case name == "git" && args[0] == "status":
		if f.dirty {
			return []byte(" M synthetic"), nil
		}
		return nil, nil
	case name == "go" && args[0] == "version":
		return []byte("go version go1.27.1 linux/amd64"), nil
	case name == "go" && args[0] == "build":
		for i, arg := range args {
			if arg == "-o" {
				return nil, os.WriteFile(args[i+1], []byte("synthetic executable"), 0700)
			}
		}
	}
	return nil, errors.New("unexpected command, including executable invocation")
}

func fixture(t *testing.T) (options, *packageFake) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return options{Version: "1.0.0", Output: filepath.Join(parent, "release"), Targets: "darwin/arm64,linux/amd64,windows/arm64", Mode: "distribution"}, &packageFake{commit: strings.Repeat("b", 40)}
}

func TestPortableArchivesAndChecksums(t *testing.T) {
	o, f := fixture(t)
	if err := buildRelease(context.Background(), o, f.run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.Output, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Qualification != "unsigned-build-only" || len(manifest.Artifacts) != 3 || manifest.Commit != f.commit {
		t.Fatal("incorrect manifest")
	}
	for _, artifact := range manifest.Artifacts {
		path := filepath.Join(o.Output, artifact.Artifact)
		digest, err := fileDigest(path)
		if err != nil || digest != artifact.SHA256 {
			t.Fatal("archive digest mismatch", err)
		}
		archive, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, file := range archive.File {
			found[filepath.Base(file.Name)] = true
			if filepath.Base(file.Name) == "telegram-mcp" && file.Mode().Perm() != 0700 {
				t.Fatal("missing Unix executable permissions")
			}
			if strings.Contains(file.Name, "\\") {
				t.Fatal("nonportable archive name")
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		suffix := ""
		if strings.HasPrefix(artifact.Platform, "windows") {
			suffix = ".exe"
		}
		for _, name := range []string{"telegram-mcp" + suffix, "telegram-mcpctl" + suffix, "telegram-mcpd" + suffix, "SHA256SUMS", "README.md"} {
			if !found[name] {
				t.Fatal("missing payload", name)
			}
		}
	}
	calls := strings.Join(f.calls, "\n")
	for _, target := range []string{"GOOS=darwin GOARCH=arm64", "GOOS=linux GOARCH=amd64", "GOOS=windows GOARCH=arm64"} {
		if !strings.Contains(calls, target) {
			t.Fatal("missing target", target)
		}
	}
	if err := buildRelease(context.Background(), o, f.run); err == nil {
		t.Fatal("reused output")
	}
}

func TestFailureNeverProducesManifest(t *testing.T) {
	o, f := fixture(t)
	if err := buildRelease(context.Background(), o, f.run); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= len(f.calls); i++ {
		trial := o
		trial.Output = filepath.Join(filepath.Dir(o.Output), fmt.Sprint("failure-", i))
		fake := &packageFake{commit: f.commit, failAt: i}
		if err := buildRelease(context.Background(), trial, fake.run); err == nil {
			t.Fatal("ignored failure", i)
		}
		if _, err := os.Stat(filepath.Join(trial.Output, "release.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failure marked complete", i)
		}
	}
}

func TestDirtySourceExplicitDevelopmentOnly(t *testing.T) {
	o, f := fixture(t)
	f.dirty = true
	if err := buildRelease(context.Background(), o, f.run); err == nil {
		t.Fatal("dirty release accepted")
	}
	o.Mode = "development"
	if err := buildRelease(context.Background(), o, f.run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.Output, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-dirty") || !strings.Contains(string(data), "development-build-only") {
		t.Fatal("dirty build mislabeled")
	}
}

func TestRejectInvalidTargets(t *testing.T) {
	o, _ := fixture(t)
	for _, targets := range []string{"", "windows/386", "darwin/arm64,darwin/arm64", "linux/amd64,", "linux/amd64/extra"} {
		o.Targets = targets
		if err := o.validate(); err == nil {
			t.Fatal("accepted", targets)
		}
	}
}

func TestRejectDocumentationOutput(t *testing.T) {
	o, f := fixture(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	o.Output = filepath.Join(root, "docs", "synthetic-output")
	if err := buildRelease(context.Background(), o, f.run); err == nil {
		t.Fatal("recursive documentation copy accepted")
	}
	if _, err := os.Stat(o.Output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe output created")
	}
}
