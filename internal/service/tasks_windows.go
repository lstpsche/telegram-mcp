package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lstpsche/telegram-mcp/internal/privatefs"
	"golang.org/x/sys/windows"
)

func validateUser(home string, uid int) error {
	if uid != os.Geteuid() {
		return errors.New("service requires the current user")
	}
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func (m *Manager) stateDir() string {
	if m.storageDir != "" {
		return m.storageDir
	}
	return filepath.Join(m.home, "AppData", "Roaming", "Telegram MCP")
}

func (m *Manager) taskPath() string { return filepath.Join(m.stateDir(), "service.xml") }

func binaryConfig(dir string) Config {
	return Config{dir, filepath.Join(dir, "telegram-mcp.exe"), filepath.Join(dir, "telegram-mcpd.exe"), filepath.Join(dir, "telegram-mcpctl.exe")}
}

func (m *Manager) inspectBinaries(ctx context.Context, dir string) (Config, error) {
	if strings.Contains(dir, "%") {
		return Config{}, ErrUnsafePath
	}
	if err := validatePath(dir); err != nil {
		return Config{}, err
	}
	if err := privatefs.CheckDirectory(dir); err != nil {
		return Config{}, err
	}
	config := binaryConfig(dir)
	for _, path := range []string{config.Relay, config.Daemon, config.Control} {
		if err := privatefs.CheckFile(path); err != nil {
			return Config{}, err
		}
	}
	return config, ctx.Err()
}

func currentSID() (string, error) {
	sid, err := privatefs.CurrentUserSID()
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

func (m *Manager) taskName() (string, error) {
	sid, err := currentSID()
	if err != nil {
		return "", err
	}
	return Label + "-" + sid, nil
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (m *Manager) task(config Config) ([]byte, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
<Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + sid + `</UserId></LogonTrigger></Triggers>
<Principals><Principal id="Owner"><UserId>` + sid + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
<Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled></Settings>
<Actions Context="Owner"><Exec><Command>` + xmlText(config.Daemon) + `</Command></Exec></Actions>
</Task>
`), nil
}

func (m *Manager) recordPath() string { return m.taskPath() }

func (m *Manager) readRegistration(ctx context.Context) (Config, error) {
	data, err := privatefs.ReadFile(m.taskPath(), maxInstallationBytes)
	if err != nil {
		return Config{}, err
	}
	var task struct {
		Command string `xml:"Actions>Exec>Command"`
	}
	if err := xml.Unmarshal(data, &task); err != nil {
		return Config{}, ErrInvalidInstallation
	}
	if strings.Contains(task.Command, "%") {
		return Config{}, ErrUnsafePath
	}
	config := binaryConfig(filepath.Dir(task.Command))
	expected, err := m.task(config)
	if err != nil {
		return Config{}, err
	}
	if !bytes.Equal(data, expected) {
		return Config{}, ErrInvalidInstallation
	}
	return config, ctx.Err()
}

func systemProgram(name string) (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (m *Manager) schtasks(ctx context.Context, args ...string) error {
	program, err := systemProgram("schtasks.exe")
	if err != nil {
		return err
	}
	return m.runner.Run(ctx, program, args...)
}

func (m *Manager) installRegistration(ctx context.Context, config Config) error {
	state, err := m.taskState(ctx)
	if err != nil {
		return err
	}
	if state != 4 {
		return ErrServiceLoaded
	}
	data, err := m.task(config)
	if err != nil {
		return err
	}
	if err := privatefs.WriteFile(m.taskPath(), data, false); err != nil {
		return err
	}
	return m.activateRegistration(ctx)
}

func (m *Manager) activateRegistration(ctx context.Context) error {
	name, err := m.taskName()
	if err != nil {
		return err
	}
	return m.schtasks(ctx, "/Create", "/TN", name, "/XML", m.taskPath())
}

func (m *Manager) taskState(ctx context.Context) (int, error) {
	name, err := m.taskName()
	if err != nil {
		return 0, err
	}
	program, err := systemProgram(`WindowsPowerShell\v1.0\powershell.exe`)
	if err != nil {
		return 0, err
	}
	// Only a fixed label and Windows-generated SID enter the script.
	if strings.ContainsAny(name, "'\r\n") {
		return 0, ErrInvalidInstallation
	}
	script := `$ErrorActionPreference='Stop'; try { $t=Get-ScheduledTask -TaskPath '\' | Where-Object { $_.TaskName -eq '` + name + `' }; if ($null -eq $t) { exit 4 }; if ($t.State -in @('Running','Queued')) { exit 0 }; if ($t.State -in @('Ready','Disabled')) { exit 3 }; exit 5 } catch { exit 5 }`
	err = m.runner.Run(ctx, program, "-NoProfile", "-NonInteractive", "-Command", script)
	if err == nil {
		return 0, nil
	}
	if ctx.Err() == nil {
		if exited(err, 3) {
			return 3, nil
		}
		if exited(err, 4) {
			return 4, nil
		}
	}
	return 0, err
}

func (m *Manager) running(ctx context.Context) (bool, error) {
	state, err := m.taskState(ctx)
	return state == 0, err
}

func (m *Manager) startRegistration(ctx context.Context) error {
	name, err := m.taskName()
	if err != nil {
		return err
	}
	return m.schtasks(ctx, "/Run", "/TN", name)
}

func (m *Manager) stopRegistration(ctx context.Context) error {
	name, err := m.taskName()
	if err != nil {
		return err
	}
	return m.schtasks(ctx, "/End", "/TN", name)
}

func (m *Manager) removeRegistration(ctx context.Context) error {
	name, err := m.taskName()
	if err != nil {
		return err
	}
	state, err := m.taskState(ctx)
	if err != nil {
		return err
	}
	if state != 4 {
		if err := m.schtasks(ctx, "/Delete", "/TN", name, "/F"); err != nil {
			return err
		}
	}
	return os.Remove(m.taskPath())
}

func defaultAccountLock(home string) string {
	return filepath.Join(home, "AppData", "Roaming", "Telegram MCP", "account.lock")
}

func (m *Manager) registrationExists(ctx context.Context) (bool, error) {
	state, err := m.taskState(ctx)
	return state != 4, err
}
