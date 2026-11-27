//go:build !windows

package winautostart

import "github.com/GecEnterprises/corekiosk/go/ckframework"

func TaskName(appName string) string {
	return "CoreKiosk_" + appName
}

func Status(appName string) (installed bool, taskName string, detail string) {
	return false, TaskName(appName), "Windows only"
}

func Install(_, _ string) error {
	return ckframework.ErrValidation.New("winautostart is only supported on Windows")
}

func Uninstall(_ string) error {
	return ckframework.ErrValidation.New("winautostart is only supported on Windows")
}
