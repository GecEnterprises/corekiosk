//go:build windows

package winautostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
)

const (
	createNoWindow        = 0x08000000
	seeMaskNoCloseProcess = 0x00000040
	swHide                = 0
	infiniteTimeout       = 0xFFFFFFFF
	runRegistryKey        = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	builtinUsersReadGrant = `*S-1-5-32-545:(R)`
)

var (
	modShell32             = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteExW    = modShell32.NewProc("ShellExecuteExW")
	modKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procWaitForSingleObj   = modKernel32.NewProc("WaitForSingleObject")
	procGetExitCodeProcess = modKernel32.NewProc("GetExitCodeProcess")
	procCloseHandle        = modKernel32.NewProc("CloseHandle")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     syscall.Handle
}

func TaskName(appName string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, appName)
	return "CoreKiosk_" + clean
}

func runHidden(bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return cmd.CombinedOutput()
}

// runElevated launches bin with params via the Windows UAC "runas" verb and waits for completion.
func runElevated(bin, params string) (uint32, error) {
	verbPtr, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return 1, err
	}
	filePtr, err := syscall.UTF16PtrFromString(bin)
	if err != nil {
		return 1, err
	}
	paramPtr, err := syscall.UTF16PtrFromString(params)
	if err != nil {
		return 1, err
	}

	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verbPtr,
		lpFile:       filePtr,
		lpParameters: paramPtr,
		nShow:        swHide,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if ret == 0 {
		return 1, fmt.Errorf("UAC elevation cancelled or denied (%v)", callErr)
	}
	if sei.hProcess != 0 {
		defer procCloseHandle.Call(uintptr(sei.hProcess))
		procWaitForSingleObj.Call(uintptr(sei.hProcess), uintptr(infiniteTimeout))
		var exitCode uint32
		procGetExitCodeProcess.Call(uintptr(sei.hProcess), uintptr(unsafe.Pointer(&exitCode)))
		if exitCode != 0 {
			return exitCode, fmt.Errorf("elevated command exited with code %d", exitCode)
		}
	}
	return 0, nil
}

func isTaskInstalled(tn string) bool {
	out, err := runHidden("schtasks", "/Query", "/TN", tn)
	if err == nil {
		return true
	}
	// If the task was created with /RL HIGHEST and DACLs weren't opened, non-admin schtasks
	// returns "Access is denied" (whereas a non-existent task returns "cannot find the file").
	lower := strings.ToLower(string(out))
	if strings.Contains(lower, "access is denied") {
		return true
	}
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	if _, statErr := os.Stat(filepath.Join(sysRoot, "System32", "Tasks", tn)); statErr == nil {
		return true
	}
	return false
}

func isRegistryRunInstalled(tn string) bool {
	_, err := runHidden("reg", "query", runRegistryKey, "/v", tn)
	return err == nil
}

// Status checks whether autostart is installed in Windows Task Scheduler or HKCU\...\Run.
func Status(appName string) (installed bool, taskName string, detail string) {
	tn := TaskName(appName)
	if isTaskInstalled(tn) {
		return true, tn, "Installed (Task Scheduler: " + tn + ")"
	}
	if isRegistryRunInstalled(tn) {
		return true, tn, "Installed (User Logon Registry: " + tn + ")"
	}
	return false, tn, "Not installed"
}

// Install registers Windows logon autostart using smart privilege escalation:
//  1. Attempts direct Task Scheduler registration (succeeds immediately if already running as Admin).
//  2. If not elevated, triggers a native Windows UAC prompt ("runas") to register the HIGHEST-privilege
//     Task Scheduler ONLOGON task and grants read visibility to standard users.
//  3. If UAC is declined or unavailable on a standard user account, falls back to per-user
//     HKCU\Software\Microsoft\Windows\CurrentVersion\Run registration (zero Admin rights required).
func Install(appName, exePath string) error {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return ckframework.ErrInternal.Wrap(err, "resolve launcher executable path")
	}
	tn := TaskName(appName)
	tr := fmt.Sprintf(`"%s"`, abs)

	// 1. Try direct Task Scheduler creation (if process is already elevated).
	if _, err := runHidden("schtasks", "/Create", "/SC", "ONLOGON", "/RL", "HIGHEST", "/TN", tn, "/TR", tr, "/F"); err == nil {
		grantTaskReadAccess(tn)
		return nil
	}

	// 2. Request Windows UAC elevation via ShellExecuteExW("runas") to register the scheduled task
	// and grant read permission on the task file so non-elevated UI can query it cleanly.
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	taskFile := filepath.Join(sysRoot, "System32", "Tasks", tn)
	elevatedCmd := fmt.Sprintf(
		`/C schtasks /Create /SC ONLOGON /RL HIGHEST /TN "%s" /TR "\"%s\"" /F && icacls "%s" /grant "%s" >nul 2>&1`,
		tn, abs, taskFile, builtinUsersReadGrant,
	)
	if _, uacErr := runElevated("cmd.exe", elevatedCmd); uacErr == nil && isTaskInstalled(tn) {
		return nil
	}

	// 3. Fallback: per-user Registry logon autostart (requires no Administrator privileges).
	regOut, regErr := runHidden("reg", "add", runRegistryKey, "/v", tn, "/t", "REG_SZ", "/d", tr, "/f")
	if regErr != nil {
		return ckframework.ErrInternal.Wrap(regErr, "autostart install failed (UAC and HKCU Run fallback): %s", strings.TrimSpace(string(regOut)))
	}
	return nil
}

// Uninstall removes the autostart entry from both HKCU\...\Run and Windows Task Scheduler
// (escalating via UAC if an elevated Task Scheduler task is present).
func Uninstall(appName string) error {
	tn := TaskName(appName)
	var lastErr error

	if isRegistryRunInstalled(tn) {
		if out, err := runHidden("reg", "delete", runRegistryKey, "/v", tn, "/f"); err != nil {
			lastErr = ckframework.ErrInternal.Wrap(err, "registry autostart remove failed: %s", strings.TrimSpace(string(out)))
		}
	}

	if isTaskInstalled(tn) {
		if _, err := runHidden("schtasks", "/Delete", "/TN", tn, "/F"); err != nil {
			// Escalate via UAC to delete the elevated scheduled task.
			params := fmt.Sprintf(`/Delete /TN "%s" /F`, tn)
			if _, uacErr := runElevated("schtasks.exe", params); uacErr != nil {
				lastErr = ckframework.ErrInternal.Wrap(uacErr, "elevated schtasks delete failed")
			}
		}
	}

	if isTaskInstalled(tn) || isRegistryRunInstalled(tn) {
		if lastErr != nil {
			return lastErr
		}
		return ckframework.ErrInternal.New("failed to remove autostart task %s", tn)
	}
	return nil
}

func grantTaskReadAccess(tn string) {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	taskFile := filepath.Join(sysRoot, "System32", "Tasks", tn)
	_, _ = runHidden("icacls", taskFile, "/grant", builtinUsersReadGrant)
}
