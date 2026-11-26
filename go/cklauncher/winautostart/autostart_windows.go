//go:build windows

package winautostart

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

func taskName(appName string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, appName)
	return "CoreKiosk_" + clean
}

// Install registers a Windows Task Scheduler "At log on" interactive-session autostart task
// (not a Session 0 service, so GPU/DirectX/Leap/webcam access works cleanly).
func Install(appName, exePath string) error {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "resolve launcher executable path")
	}
	tn := taskName(appName)
	tr := fmt.Sprintf(`"%s"`, abs)
	out, err := exec.Command("schtasks", "/Create",
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/TN", tn,
		"/TR", tr,
		"/F",
	).CombinedOutput()
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "schtasks install failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes the registered Task Scheduler logon task.
func Uninstall(appName string) error {
	tn := taskName(appName)
	out, err := exec.Command("schtasks", "/Delete", "/TN", tn, "/F").CombinedOutput()
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "schtasks uninstall failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
