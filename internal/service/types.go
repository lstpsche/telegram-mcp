package service

import (
	"errors"
	"fmt"
)

const Label = "dev.telegram-mcp.gateway"

var (
	ErrNotInstalled       = errors.New("service is not installed")
	ErrAlreadyInstalled   = errors.New("service is already installed")
	ErrServiceLoaded      = errors.New("service is already registered")
	ErrServiceAbsent      = errors.New("service is not registered")
	ErrDisabled           = errors.New("service startup is disabled")
	ErrMasked             = fmt.Errorf("service startup is masked: %w", ErrDisabled)
	ErrServiceUnavailable = errors.New("user service manager is unavailable")
	ErrStartupState       = errors.New("service startup coordination is invalid")
	ErrSessionClosed      = errors.New("service lifecycle session is closed")
)

// Config records the exact binaries selected by the installed service configuration.
type Config struct {
	BinDir string
	Relay  string
	Daemon string
}

// StartDisposition distinguishes a submitted launch from an existing attempt.
type StartDisposition string

const (
	StartLaunched StartDisposition = "launched"
	StartJoining  StartDisposition = "joining"
)
