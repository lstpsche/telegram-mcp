package diagnostics

import "github.com/lstpsche/telegram-mcp/internal/privatefs"

// OS-owned ancestors use the shared safe-ancestry check; only actual runtime
// directories require the stricter owner-only DACL enforced by inspectFiles.
func inspectAncestors(path string) error { return privatefs.CheckAncestors(path) }
