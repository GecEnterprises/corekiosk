//go:build !windows

package winautostart

import "github.com/GecEnterprises/corekiosk/go/ckframework"

func Install(_, _ string) error {
	return ckframework.ErrValidation.New("winautostart is only supported on Windows")
}

func Uninstall(_ string) error {
	return ckframework.ErrValidation.New("winautostart is only supported on Windows")
}
