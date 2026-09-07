package control

import (
	"bytes"
	"context"
	"testing"

	"github.com/lstpsche/telegram-mcp/internal/model"
	"github.com/lstpsche/telegram-mcp/internal/policy"
)

type folderController struct {
	fakeScopeController
	folder int32
}

func (f *folderController) Folders(_ context.Context, id int32) ([]model.Folder, error) {
	f.folder = id
	return []model.Folder{{ID: 2, Title: "synthetic"}}, nil
}
func (f *folderController) ScopeFromFolder(ctx context.Context, id model.ScopeID, name string, folder int32) (policy.Scope, error) {
	f.folder = folder
	return f.Scope(ctx, id, name, nil)
}
func TestFolderCommandsPreviewAndImport(t *testing.T) {
	f := &folderController{}
	for _, args := range [][]string{{"folders"}, {"folders", "--id", "2"}, {"scope", "--name", "work", "--folder", "2"}} {
		var out, err bytes.Buffer
		code := runContext(context.Background(), args, &out, &err, func() (controller, error) { return f, nil }, nil)
		if code != 0 || out.Len() == 0 || err.Len() != 0 {
			t.Fatal("command failed", code, err.String())
		}
	}
	if f.folder != 2 || f.saved.Name != "work" {
		t.Fatal("folder selection lost")
	}
	for _, args := range [][]string{{"folders", "--id", "01"}, {"scope", "--name", "work", "--folder", "1"}, {"scope", "--name", "work", "--folder", "2", "--folder", "3"}, {"scope", "--name", "work", "--folder", "2", "--peer", "tgpeer:v1:chat:1"}} {
		var out, err bytes.Buffer
		if code := runContext(context.Background(), args, &out, &err, func() (controller, error) { return f, nil }, nil); code != 2 || out.Len() != 0 {
			t.Fatal("invalid selection accepted")
		}
	}
}
