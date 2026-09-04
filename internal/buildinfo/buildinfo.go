// Package buildinfo owns the small amount of immutable build metadata shared
// by TgContext binaries.
package buildinfo

import "fmt"

const Product = "TgContext"

// These values may be replaced with -ldflags for release builds.
var (
	Version = "dev"
	Commit  = "unknown"
)

// String returns a content-free, stable version line for one binary.
func String(component string) string {
	return fmt.Sprintf("%s %s version=%s commit=%s", Product, component, Version, Commit)
}
