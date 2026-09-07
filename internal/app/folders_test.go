package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/daemon"
	"github.com/lstpsche/telegram-mcp/internal/model"
)

type folderRuntime struct {
	fakeDiscoveryRuntime
	folders []model.Folder
	err     error
}

func (f *folderRuntime) DiscoverFolders(context.Context, int32) ([]model.Folder, error) {
	return f.folders, f.err
}

func TestFolderImportIsAtomicSelectionWithoutAuthority(t *testing.T) {
	ctx := context.Background()
	peer, _ := model.ParsePeerID("tgpeer:v1:chat:123")
	runtime := &folderRuntime{folders: []model.Folder{{ID: 2, Title: "untrusted title", Peers: []model.PeerID{peer}}}}
	app := authorizedTextApplication(t, runtime)
	lock, err := daemon.AcquireAccountLock(app.paths.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ScopeFromFolder(ctx, "", "work", 2); !errors.Is(err, daemon.ErrAccountLocked) {
		t.Fatal("missing session lock", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	scope, err := app.ScopeFromFolder(ctx, "", "work", 2)
	if err != nil || len(scope.Peers) != 1 {
		t.Fatal("import failed", err)
	}
	grants, err := app.Grants(ctx)
	if err != nil || len(grants) != 0 {
		t.Fatal("import created authority", err)
	}
	for _, failure := range []string{"provider", "oversize"} {
		runtime.err = nil
		if failure == "provider" {
			runtime.err = errors.New("synthetic failure")
		} else {
			runtime.folders[0].Peers = make([]model.PeerID, 21)
		}
		if _, err := app.ScopeFromFolder(ctx, scope.ID, "work", 2); err == nil {
			t.Fatal("invalid import succeeded")
		}
		scopes, err := app.Scopes(ctx)
		if err != nil || len(scopes) != 1 || len(scopes[0].Peers) != 1 || scopes[0].Peers[0] != peer {
			t.Fatal("failed import changed scope", err)
		}
	}
}
