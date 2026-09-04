// Package dependencycheck keeps the future MCP runtime dependency in the module
// graph until the stdio/socket transport phase imports it directly.
package dependencycheck

import (
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
