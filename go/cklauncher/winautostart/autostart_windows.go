//go:build windows

package winautostart

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

const createNoWindow = 0x08000000

func TaskName(appName string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, appName)
	return "CoreKiosk_" + clean
}

func runSchtasks(args ...string) ([]byte, error) {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd.CombinedOutput()
}

// Status checks whether the Windows Task Scheduler logon autostart task is currently installed.
func Status(appName string) (installed bool, taskName string, detail string) {
	tn := TaskName(appName)
	if _, err := runSchtasks("/Query", "/TN", tn); err == nil {
		return true, tn, "Installed (" + tn + ")"
	}
	return false, tn, "Not installed"
}

// Install registers a Windows Task Scheduler "At log on" interactive-session autostart task
// (not a Session 0 service, so GPU/DirectX/Leap/webcam access works cleanly).
// Tries HIGHEST runlevel first, falling back to LIMITED if the process is not elevated.
func Install(appName, exePath string) error {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "resolve launcher executable path")
	}
	tn := TaskName(appName)
	tr := fmt.Sprintf(`"%s"`, abs)
	out, err := runSchtasks("/Create",
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/TN", tn,
		"/TR", tr,
		"/F",
	)
	if err != nil {
		// Retry without /RL HIGHEST so standard non-elevated Windows accounts can also register logon autostart.
		outFallback, errFallback := runSchtasks("/Create",
			"/SC", "ONLOGON",
			"/TN", tn,
			"/TR", tr,
			"/F",
		)
		if errFallback != nil {
			return ckframework.ErrInternal.Wrap(err, "schtasks install failed: %s", strings.TrimSpace(string(out)+"; "+string(outFallback)))
		}
	}
	return nil
}

// Uninstall removes the registered Task Scheduler logon task.
func Uninstall(appName string) error {
	tn := TaskName(appName)
	out, err := runSchtasks("/Delete", "/TN", tn, "/F")
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "schtasks uninstall failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
