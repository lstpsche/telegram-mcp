package main

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/lstpsche/telegram-mcp/internal/app"
	"github.com/lstpsche/telegram-mcp/internal/store"
)

type maintenanceController interface {
	Backup(context.Context, string) error
	PreviewRestore(context.Context, string) (app.RestorePreview, error)
	Restore(context.Context, string) error
	AuditMaintenance(context.Context, *store.AuditRetention, bool, bool) (store.AuditStatus, error)
}

func runMaintenanceCommand(ctx context.Context, args []string, stdout, stderr io.Writer, control controller) int {
	usage := func() int {
		fmt.Fprintln(stderr, "telegram-mcpctl: invalid maintenance arguments; see --help")
		return 2
	}
	var retention *store.AuditRetention
	apply, purge := false, false
	switch args[0] {
	case "backup":
		if len(args) != 3 || args[1] != "--file" {
			return usage()
		}
	case "restore":
		preview := len(args) == 4 && args[1] == "--dry-run" && args[2] == "--file"
		apply := len(args) == 5 && args[1] == "--file" && args[3] == "--replace-scopes" && args[4] == "--reset-access"
		if !preview && !apply {
			return usage()
		}
	case "backup-inspect":
		if len(args) != 3 || args[1] != "--file" {
			return usage()
		}
		backup, err := app.InspectBackup(args[2])
		if err == nil {
			err = writeTextJSON(stdout, backup)
		}
		if err != nil {
			writeControlError(stderr, err)
			return 1
		}
		return 0
	case "audit":
		switch {
		case len(args) == 1:
		case len(args) == 3 && args[1] == "prune" && args[2] == "--confirm":
			apply = true
		case len(args) == 4 && args[1] == "purge" && args[2] == "--all" && args[3] == "--confirm":
			purge = true
		case len(args) == 7 && args[1] == "retention" && args[2] == "--days" && args[4] == "--max-records" && args[6] == "--apply":
			days, err := strconv.Atoi(args[3])
			if err != nil || strconv.Itoa(days) != args[3] {
				return usage()
			}
			max, err := strconv.Atoi(args[5])
			if err != nil || strconv.Itoa(max) != args[5] {
				return usage()
			}
			retention = &store.AuditRetention{Days: days, MaxRecords: max}
			if retention.Validate() != nil {
				return usage()
			}
			apply = true
		default:
			return usage()
		}
	default:
		return usage()
	}
	maintenance, ok := control.(maintenanceController)
	if !ok {
		fmt.Fprintln(stderr, "telegram-mcpctl: metadata maintenance is unavailable")
		return 1
	}
	var err error
	switch args[0] {
	case "backup":
		err = maintenance.Backup(ctx, args[2])
		if err == nil {
			_, err = fmt.Fprintln(stdout, "Metadata backup created; credentials, permissions, synchronization state and audit history are excluded.")
		}
	case "restore":
		if args[1] == "--dry-run" {
			var preview app.RestorePreview
			preview, err = maintenance.PreviewRestore(ctx, args[3])
			if err == nil {
				err = writeTextJSON(stdout, preview)
			}
		} else {
			err = maintenance.Restore(ctx, args[2])
			if err == nil {
				_, err = fmt.Fprintln(stdout, "Scopes restored with new IDs. Access is restricted with no grants; enable access explicitly when ready.")
			}
		}
	case "audit":
		var status store.AuditStatus
		status, err = maintenance.AuditMaintenance(ctx, retention, apply, purge)
		if err == nil {
			err = writeTextJSON(stdout, status)
		}
	}
	if err != nil {
		writeControlError(stderr, err)
		return 1
	}
	return 0
}
