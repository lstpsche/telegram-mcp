package service

import (
	"bytes"
	"context"
	"encoding/base64"
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
	return Config{dir, filepath.Join(dir, "telegram-mcp.exe"), filepath.Join(dir, "telegram-mcpd.exe")}
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
	for _, path := range []string{config.Relay, config.Daemon} {
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
	return []byte(`<?xml version="1.0"?>
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

func (m *Manager) installRegistrationState(ctx context.Context, config Config, enabled bool) error {
	state, err := m.taskState(ctx)
	if err != nil {
		return err
	}
	if state != taskMissing {
		return ErrServiceLoaded
	}
	data, err := m.task(config)
	if err != nil {
		return err
	}
	if err := privatefs.WriteFile(m.taskPath(), data, false); err != nil {
		return err
	}
	return m.activateRegistrationState(ctx, enabled)
}

func (m *Manager) activateRegistrationState(ctx context.Context, enabled bool) error {
	if _, err := m.readRegistration(ctx); err != nil {
		return err
	}
	data, err := privatefs.ReadFile(m.taskPath(), maxInstallationBytes)
	if err != nil {
		return err
	}
	setting := "false"
	if enabled {
		setting = "true"
	}
	// Change only the scheduler copy's operational enabled setting. The saved
	// record stays canonical; TASK_CREATE refuses replacement and stores no password.
	return m.runTaskScript(ctx, `$xml=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('`+base64.StdEncoding.EncodeToString(data)+`'))
    $document=[xml]$xml
    $namespace=[Xml.XmlNamespaceManager]::new($document.NameTable)
    $namespace.AddNamespace('t','http://schemas.microsoft.com/windows/2004/02/mit/task')
    $document.SelectSingleNode('/t:Task/t:Settings/t:Enabled',$namespace).InnerText='`+setting+`'
    $null=$folder.RegisterTask($name, $document.OuterXml, 2, $null, $null, 3, $null)`)
}

func (m *Manager) runTaskScript(ctx context.Context, action string) error {
	name, err := m.taskName()
	if err != nil {
		return err
	}
	// Only a fixed label and Windows-generated SID enter the script.
	if strings.ContainsAny(name, "'\r\n") {
		return ErrInvalidInstallation
	}
	program, err := systemProgram(`WindowsPowerShell\v1.0\powershell.exe`)
	if err != nil {
		return err
	}
	script := `$ErrorActionPreference='Stop'
try {
    $scheduler=New-Object -ComObject 'Schedule.Service'
    $scheduler.Connect()
    $folder=$scheduler.GetFolder('\')
    $name='` + name + `'
    ` + action + `
} catch {
    $cause=$_.Exception
    while ($null -ne $cause.InnerException) { $cause=$cause.InnerException }
    exit $cause.HResult
}`
	return m.runner.Run(ctx, program, "-NoProfile", "-NonInteractive", "-Command", script)
}

const (
	taskRunning  = 0
	taskReady    = 3
	taskMissing  = 4
	taskDisabled = 6
	taskQueued   = 7
	taskInvalid  = 8
)

const findTaskScript = `try { $task=$folder.GetTask($name) } catch {
        $cause=$_.Exception
        while ($null -ne $cause.InnerException) { $cause=$cause.InnerException }
        if ($cause.HResult -eq -2147024894) { exit 4 }
        throw
    }
`

// Validate the actual scheduler definition rather than trusting the saved XML.
// Task Scheduler normalizes defaults and principal names, so compare semantics.
func (m *Manager) taskValidation(ctx context.Context) (string, error) {
	config, err := m.readRegistration(ctx)
	if err != nil {
		return "", err
	}
	expected := base64.StdEncoding.EncodeToString([]byte(config.Daemon))
	return findTaskScript + `
    function Resolve-Sid([string]$identity) {
        if ([String]::IsNullOrEmpty($identity)) { return '' }
        if ($identity.StartsWith('S-', [StringComparison]::OrdinalIgnoreCase)) {
            return [Security.Principal.SecurityIdentifier]::new($identity).Value
        }
        return [Security.Principal.NTAccount]::new($identity).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    $definition=$task.Definition
    $owner=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $expected=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + expected + `'))
    $principal=$definition.Principal
    if ((Resolve-Sid $principal.UserId) -cne $owner -or
        $principal.Id -cne 'Owner' -or
        $principal.LogonType -ne 3 -or $principal.RunLevel -ne 0) { exit 8 }
    if ($definition.Actions.Count -ne 1 -or $definition.Actions.Context -cne 'Owner') { exit 8 }
    $action=$definition.Actions.Item(1)
    if ($action.Type -ne 0 -or
        -not [String]::Equals($action.Path,$expected,[StringComparison]::OrdinalIgnoreCase) -or
        -not [String]::IsNullOrEmpty($action.Arguments) -or
        -not [String]::IsNullOrEmpty($action.WorkingDirectory)) { exit 8 }
    if ($definition.Triggers.Count -ne 1) { exit 8 }
    $trigger=$definition.Triggers.Item(1)
    if ($trigger.Type -ne 9 -or -not $trigger.Enabled -or
        (Resolve-Sid $trigger.UserId) -cne $owner -or
        -not [String]::IsNullOrEmpty($trigger.StartBoundary) -or
        -not [String]::IsNullOrEmpty($trigger.EndBoundary) -or
        ([String]$trigger.Delay) -notin @('', 'PT0S') -or
        ([String]$trigger.ExecutionTimeLimit) -notin @('', 'PT0S') -or
        -not [String]::IsNullOrEmpty($trigger.Repetition.Interval) -or
        -not [String]::IsNullOrEmpty($trigger.Repetition.Duration)) { exit 8 }
    $settings=$definition.Settings
    if (-not $settings.AllowDemandStart -or $settings.MultipleInstances -ne 2 -or
        $settings.DisallowStartIfOnBatteries -or $settings.StopIfGoingOnBatteries -or
        -not $settings.AllowHardTerminate -or -not $settings.StartWhenAvailable -or
        $settings.RunOnlyIfIdle -or $settings.RunOnlyIfNetworkAvailable -or $settings.RestartCount -ne 0 -or
        $settings.ExecutionTimeLimit -cne 'PT0S') { exit 8 }
`, nil
}

func (m *Manager) checkedTask(ctx context.Context, action string) error {
	validation, err := m.taskValidation(ctx)
	if err != nil {
		return err
	}
	err = m.runTaskScript(ctx, validation+action)
	if ctx.Err() == nil {
		switch {
		case exited(err, taskMissing):
			return errors.Join(ErrServiceAbsent, err)
		case exited(err, taskInvalid):
			return errors.Join(ErrInvalidInstallation, err)
		case exited(err, taskDisabled):
			return errors.Join(ErrDisabled, err)
		}
	}
	return err
}

func (m *Manager) taskState(ctx context.Context) (int, error) {
	err := m.runTaskScript(ctx, findTaskScript+`
    switch ($task.State) {
        4 { exit 0 }
        2 { exit 7 }
        1 { exit 6 }
        3 { if ($task.Enabled) { exit 3 } else { exit 6 } }
        default { exit 5 }
    }`)
	if err == nil {
		return taskRunning, nil
	}
	if ctx.Err() == nil {
		for _, state := range []int{taskReady, taskMissing, taskDisabled, taskQueued} {
			if exited(err, state) {
				return state, nil
			}
		}
	}
	return 0, err
}

func (m *Manager) running(ctx context.Context) (bool, error) {
	state, err := m.taskState(ctx)
	return state == taskRunning || state == taskQueued, err
}

func (m *Manager) enabled(ctx context.Context) (bool, error) {
	err := m.checkedTask(ctx, `
    if (-not $task.Enabled) { exit 6 }`)
	if errors.Is(err, ErrDisabled) {
		return false, nil
	}
	return err == nil, err
}

func (m *Manager) setEnabled(ctx context.Context, enabled bool) error {
	setting := "$false"
	if enabled {
		setting = "$true"
	}
	return m.checkedTask(ctx, "\n    $task.Enabled="+setting)
}

// One validated query classifies both permission and activity. A later wake
// revalidates the task before submitting the actual demand start.
func (m *Manager) startupState(ctx context.Context) (bool, error) {
	err := m.checkedTask(ctx, `
    if (-not $task.Enabled) { exit 6 }
    switch ($task.State) {
        4 { exit 0 }
        2 { exit 0 }
        3 { exit 3 }
        default { exit 5 }
    }`)
	if ctx.Err() == nil && exited(err, taskReady) {
		return false, nil
	}
	return err == nil, err
}

func (m *Manager) wake(ctx context.Context) error {
	return m.checkedTask(ctx, `
    if (-not $task.Enabled) { exit 6 }
    if ($task.State -eq 3) { $null=$task.Run($null) }
    elseif ($task.State -notin @(2,4)) { exit 5 }`)
}

func (m *Manager) stopRegistration(ctx context.Context) error {
	return m.checkedTask(ctx, `
    $task.Stop(0)`)
}

func (m *Manager) removeRegistration(ctx context.Context) error {
	state, err := m.taskState(ctx)
	if err != nil {
		return err
	}
	if state != taskMissing {
		if err := m.checkedTask(ctx, `
    $folder.DeleteTask($name, 0)`); err != nil {
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
	return state != taskMissing, err
}
