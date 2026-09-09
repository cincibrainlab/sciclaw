package tui

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sipeed/picoclaw/pkg/paths"
)

// VMExecutor runs commands inside the multipass VM.
type VMExecutor struct{}

func (e *VMExecutor) Mode() Mode { return ModeVM }

func (e *VMExecutor) ExecShell(timeout time.Duration, shellCmd string) (string, error) {
	return VMExecShell(timeout, shellCmd)
}

func (e *VMExecutor) ExecCommand(timeout time.Duration, args ...string) (string, error) {
	return VMExec(timeout, args...)
}

func (e *VMExecutor) ReadFile(path string) (string, error) {
	return VMCatFile(path)
}

func (e *VMExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	cmd := vmWriteFileCommand(path, data, perm)
	_, err := VMExecShell(10*time.Second, cmd)
	return err
}

func vmWriteFileCommand(path string, data []byte, perm os.FileMode) string {
	return "umask 077; printf '%s' " + shellEscape(string(data)) + " > " + shellEscape(path) + "; chmod " + fmt.Sprintf("%04o", perm.Perm()) + " " + shellEscape(path)
}

func (e *VMExecutor) ConfigPath() string { return "/home/ubuntu/sciclaw/config.json" }
func (e *VMExecutor) AuthPath() string   { return "/home/ubuntu/.sciclaw/auth.json" }
func (e *VMExecutor) HomePath() string   { return "/home/ubuntu" }
func (e *VMExecutor) BinaryPath() string { return "sciclaw" } // VM always uses PATH

func (e *VMExecutor) AgentVersion() string {
	return VMAgentVersion()
}

func (e *VMExecutor) ServiceInstalled() bool {
	return VMServiceInstalled()
}

func (e *VMExecutor) ServiceActive() bool {
	return VMServiceActive()
}

func (e *VMExecutor) InteractiveProcess(args ...string) *exec.Cmd {
	withEnv := append([]string{"env", "HOME=/home/ubuntu"}, args...)
	full := append([]string{"exec", vmName, "--"}, withEnv...)
	c := exec.Command("multipass", full...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

// VMInfoSnapshot returns VM-specific info (state, IP, load, memory, mounts).
// Only meaningful in VM mode.
func (e *VMExecutor) VMInfoSnapshot() VMInfo {
	return GetVMInfo()
}

// HostConfigRaw reads the host-side config for drift detection.
func (e *VMExecutor) HostConfigRaw() (string, error) {
	if err := paths.ValidateHomes(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(paths.ConfigPath())
	if err != nil {
		return "", err
	}
	return string(b), nil
}
