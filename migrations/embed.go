// Package migrations embeds Telegram MCP's forward-only SQLite schema changes.
package migrations

import "embed"

// Files contains only numbered SQL migrations at its root.
//
//go:embed *.sql
var Files embed.FS
