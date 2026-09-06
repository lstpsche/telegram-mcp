package service

import "errors"

const Label = "dev.telegram-mcp.gateway"

var (
	ErrNotInstalled     = errors.New("service is not installed")
	ErrAlreadyInstalled = errors.New("service is already installed")
	ErrServiceLoaded    = errors.New("service is already registered")
	ErrServiceAbsent    = errors.New("service is not registered")
)

// Config records the exact binaries selected by the installed service configuration.
type Config struct {
	BinDir string
	Relay  string
	Daemon string
}
