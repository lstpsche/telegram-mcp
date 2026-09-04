// Package dependencycheck keeps future runtime dependencies in the module graph
// and compiles their selected public packages during Phase 0. Delete each blank
// import when production code begins importing that module directly.
package dependencycheck

import (
	_ "github.com/gotd/td/telegram"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
