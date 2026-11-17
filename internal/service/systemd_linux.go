package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func validateUser(home string, uid int) error {
	if uid <= 0 || uid != os.Geteuid() {
		return errors.New("service requires the current non-root user")
	}
	return inspectDirectory(home, uid, false)
}

func (m *Manager) stateDir() string {
	if m.storageDir != "" {
		return m.storageDir
	}
	return filepath.Join(m.home, ".config", "Telegram MCP")
}

func (m *Manager) unitPath() string {
	return filepath.Join(filepath.Dir(m.stateDir()), "systemd", "user", Label+".service")
}

func binaryConfig(dir string) Config {
	return Config{dir, filepath.Join(dir, "telegram-mcp"), filepath.Join(dir, "telegram-mcpd")}
}

func (m *Manager) unit(config Config) []byte {
	// systemd expands percent specifiers even inside quoted command arguments.
	command := strconv.Quote(strings.ReplaceAll(config.Daemon, "%", "%%"))
	environment := "Environment=" + strconv.Quote("XDG_CONFIG_HOME="+strings.ReplaceAll(filepath.Dir(m.stateDir()), "%", "%%")) + "\nEnvironment=" + strconv.Quote("XDG_CACHE_HOME="+strings.ReplaceAll(m.cacheRoot, "%", "%%")) + "\n"
	return []byte("[Unit]\nDescription=Telegram MCP\n\n[Service]\nType=simple\nExecStart=:" + command + "\n" + environment + "UMask=0077\nTimeoutStopSec=20\nStandardOutput=null\nStandardError=null\n\n[Install]\nWantedBy=default.target\n")
}

func (m *Manager) recordPath() string { return m.unitPath() }

func (m *Manager) readRegistration(ctx context.Context) (Config, error) {
	if err := inspectAncestors(filepath.Dir(m.unitPath()), m.uid); err != nil {
		return Config{}, err
	}
	data, err := readUnit(m.unitPath())
	if err != nil {
		return Config{}, err
	}
	var command string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			command = strings.TrimPrefix(line, "ExecStart=")
			break
		}
	}
	executable, err := strconv.Unquote(strings.TrimPrefix(command, ":"))
	if err != nil {
		return Config{}, ErrInvalidInstallation
	}
	executable = strings.ReplaceAll(executable, "%%", "%")
	config := binaryConfig(filepath.Dir(executable))
	if !bytes.Equal(data, m.unit(config)) {
		return Config{}, ErrInvalidInstallation
	}
	return config, ctx.Err()
}

func (m *Manager) installRegistrationState(ctx context.Context, config Config, enabled bool) error {
	if err := ensureDirectory(filepath.Dir(m.unitPath()), m.uid, false); err != nil {
		return err
	}
	if err := writeExclusive(m.unitPath(), m.unit(config)); err != nil {
		return err
	}
	return m.activateRegistrationState(ctx, enabled)
}

func (m *Manager) activateRegistrationState(ctx context.Context, enabled bool) error {
	if err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return m.setEnabled(ctx, enabled)
}

func (m *Manager) running(ctx context.Context) (bool, error) {
	state, err := m.systemdState(ctx)
	if err != nil {
		return false, err
	}
	switch state.active {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, ErrServiceUnavailable
	}
}

func (m *Manager) startRegistration(ctx context.Context) error {
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "start", Label+".service")
}

func (m *Manager) stopRegistration(ctx context.Context) error {
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "stop", Label+".service")
}

func (m *Manager) removeRegistration(ctx context.Context) error {
	if err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "disable", Label+".service"); err != nil {
		return err
	}
	if err := os.Remove(m.unitPath()); err != nil {
		return err
	}
	return m.runner.Run(ctx, "/usr/bin/systemctl", "--user", "daemon-reload")
}

func defaultAccountLock(home string) string {
	return filepath.Join(home, ".config", "Telegram MCP", "account.lock")
}

func (m *Manager) registrationExists(ctx context.Context) (bool, error) {
	state, err := m.systemdState(ctx)
	if err != nil {
		return false, err
	}
	if state.load == "not-found" {
		// Explicit installation may reload a canonical saved record after an
		// interrupted registration. There must be no loaded unit or activity.
		if !systemdUnloaded(state) || (state.needsReload != "no" && state.needsReload != "yes") {
			return false, ErrInvalidInstallation
		}
		return false, nil
	}
	if err := m.validateSystemdState(state); err != nil {
		return false, err
	}
	return true, nil
}

type systemdUnitState struct {
	load, unitFile, active, fragment, dropIns, needsReload string
}

// systemdState reads only fixed, machine-readable properties. Returned bytes
// remain inside the service adapter and never become diagnostic text.
func (m *Manager) systemdState(ctx context.Context) (systemdUnitState, error) {
	query, ok := m.runner.(Queryer)
	if !ok {
		return systemdUnitState{}, ErrServiceUnavailable
	}
	output, err := query.Query(ctx, "/usr/bin/systemctl", "--user", "show", "--no-pager", "--all",
		"--property=LoadState,UnitFileState,ActiveState,FragmentPath,DropInPaths,NeedDaemonReload", Label+".service")
	if err != nil {
		return systemdUnitState{}, errors.Join(ErrServiceUnavailable, err)
	}
	if len(output) > maxQueryBytes {
		return systemdUnitState{}, ErrServiceUnavailable
	}
	properties := make(map[string]string, 6)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			return systemdUnitState{}, ErrServiceUnavailable
		}
		switch key {
		case "LoadState", "UnitFileState", "ActiveState", "FragmentPath", "DropInPaths", "NeedDaemonReload":
		default:
			return systemdUnitState{}, ErrServiceUnavailable
		}
		if _, exists := properties[key]; exists {
			return systemdUnitState{}, ErrServiceUnavailable
		}
		properties[key] = value
	}
	if len(properties) != 6 {
		return systemdUnitState{}, ErrServiceUnavailable
	}
	return systemdUnitState{properties["LoadState"], properties["UnitFileState"], properties["ActiveState"],
		properties["FragmentPath"], properties["DropInPaths"], properties["NeedDaemonReload"]}, nil
}

func (m *Manager) validateSystemdState(state systemdUnitState) error {
	if systemdAbsent(state) {
		return ErrServiceAbsent
	}
	if state.load != "loaded" || state.fragment != m.unitPath() || state.needsReload != "no" {
		return ErrInvalidInstallation
	}
	if state.unitFile != "enabled" && state.unitFile != "disabled" {
		return ErrInvalidInstallation
	}
	return validateSystemdDropIns(state.dropIns)
}

// Vendor-wide stop-timeout policy may apply even to a canonical user unit.
// Accept only the supported timeout directive from protected vendor files;
// execution, environment, restart and authority overrides remain forbidden.
func validateSystemdDropIns(paths string) error {
	if strings.ContainsAny(paths, "\r\n") {
		return ErrInvalidInstallation
	}
	seen := map[string]bool{}
	total := 0
	for _, path := range strings.Fields(paths) {
		if !vendorDropInPath(path) || seen[path] {
			return ErrInvalidInstallation
		}
		seen[path] = true
		data, err := readVendorDropIn(path)
		if err != nil {
			return errors.Join(ErrInvalidInstallation, err)
		}
		total += len(data)
		if total > maxInstallationBytes || !stopTimeoutDropIn(data) {
			return ErrInvalidInstallation
		}
	}
	return nil
}

func vendorDropInPath(path string) bool {
	if validatePath(path) != nil || !strings.HasSuffix(path, ".conf") {
		return false
	}
	switch filepath.Dir(path) {
	case "/usr/lib/systemd/user/service.d", "/usr/local/lib/systemd/user/service.d", "/lib/systemd/user/service.d":
		return true
	default:
		return false
	}
}

func stopTimeoutDropIn(data []byte) bool {
	service, configured := false, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.Trim(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if line == "[Service]" && !service {
			service = true
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !service || configured || !found || strings.Trim(key, " \t") != "TimeoutStopFailureMode" || strings.Trim(value, " \t") != "abort" {
			return false
		}
		configured = true
	}
	return configured
}

func systemdAbsent(state systemdUnitState) bool {
	return systemdUnloaded(state) && state.needsReload == "no"
}

func systemdUnloaded(state systemdUnitState) bool {
	return state.load == "not-found" && state.unitFile == "" && state.active == "inactive" &&
		state.fragment == "" && state.dropIns == ""
}

func (m *Manager) enabled(ctx context.Context) (bool, error) {
	state, err := m.systemdState(ctx)
	if err != nil {
		return false, err
	}
	if state.load == "masked" || state.unitFile == "masked" || state.unitFile == "masked-runtime" {
		return false, nil
	}
	// Human setup and upgrade may repair this unloaded record explicitly.
	// Automatic startup uses startupState and keeps reload requirements fatal.
	if systemdUnloaded(state) && state.needsReload == "yes" {
		return false, ErrServiceAbsent
	}
	if err := m.validateSystemdState(state); err != nil {
		return false, err
	}
	return state.unitFile == "enabled", nil
}

func (m *Manager) startupState(ctx context.Context) (bool, error) {
	state, err := m.systemdState(ctx)
	if err != nil {
		return false, err
	}
	if state.load == "masked" || state.unitFile == "masked" || state.unitFile == "masked-runtime" {
		return false, ErrMasked
	}
	if err := m.validateSystemdState(state); err != nil {
		return false, err
	}
	if state.unitFile != "enabled" {
		return false, ErrDisabled
	}
	switch state.active {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, ErrServiceUnavailable
	}
}

func (m *Manager) setEnabled(ctx context.Context, enabled bool) error {
	if _, err := m.readRegistration(ctx); err != nil {
		return err
	}
	state, err := m.systemdState(ctx)
	if err != nil {
		return err
	}
	if state.load == "masked" || state.unitFile == "masked" || state.unitFile == "masked-runtime" {
		// A runtime mask does not establish what will happen at the next login.
		// Do not claim a persistent change or remove an operator's mask.
		return ErrMasked
	}
	if err := m.validateSystemdState(state); err != nil {
		return err
	}
	action, expected := "disable", "disabled"
	if enabled {
		action, expected = "enable", "enabled"
	}
	if err := m.runner.Run(ctx, "/usr/bin/systemctl", "--user", action, Label+".service"); err != nil {
		return err
	}
	state, err = m.systemdState(ctx)
	if err != nil {
		return err
	}
	if err := m.validateSystemdState(state); err != nil {
		return err
	}
	if state.unitFile != expected {
		return ErrServiceUnavailable
	}
	return nil
}

func (m *Manager) wake(ctx context.Context) error {
	state, err := m.systemdState(ctx)
	if err != nil {
		return err
	}
	if state.load == "masked" || state.unitFile == "masked" || state.unitFile == "masked-runtime" {
		return ErrMasked
	}
	if err := m.validateSystemdState(state); err != nil {
		return err
	}
	if state.unitFile != "enabled" {
		return ErrDisabled
	}
	switch state.active {
	case "active", "activating", "reloading", "refreshing":
		return nil
	case "inactive", "failed":
		return m.startRegistration(ctx)
	default:
		return ErrServiceUnavailable
	}
}
