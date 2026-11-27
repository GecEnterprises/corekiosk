package cklauncher

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
)

// SupervisorStatus summarizes the child game binary process state.
type SupervisorStatus struct {
	Binary       string   `json:"binary"`
	Running      bool     `json:"running"`
	PID          int      `json:"pid,omitempty"`
	RestartCount int      `json:"restart_count"`
	LastExitCode int      `json:"last_exit_code"`
	StartedAtMs  int64    `json:"started_at_ms,omitempty"`
	RecentLogs   []string `json:"recent_logs,omitempty"`
}

// Supervisor spawns and watches the game binary, auto-restarting on crash and logging telemetry.
type Supervisor struct {
	mu            sync.Mutex
	binary        string
	extraArgs     []string
	serverURL     string
	bridgeToken   string
	autoRestart   bool
	outbox        *cktelemetry.Outbox
	cmd           *exec.Cmd
	running       bool
	pid           int
	restartCount  int
	lastExitCode  int
	startedAtMs   int64
	recentLogs    []string
	stopRequested bool
	restartCh     chan struct{}
}

func NewSupervisor(binary string, extraArgs []string, serverURL, bridgeToken string, autoRestart bool, outbox *cktelemetry.Outbox) *Supervisor {
	return &Supervisor{
		binary:      binary,
		extraArgs:   extraArgs,
		serverURL:   serverURL,
		bridgeToken: bridgeToken,
		autoRestart: autoRestart,
		outbox:      outbox,
		restartCh:   make(chan struct{}, 2),
	}
}

func (s *Supervisor) Status() SupervisorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	logs := make([]string, len(s.recentLogs))
	copy(logs, s.recentLogs)
	return SupervisorStatus{
		Binary:       s.binary,
		Running:      s.running,
		PID:          s.pid,
		RestartCount: s.restartCount,
		LastExitCode: s.lastExitCode,
		StartedAtMs:  s.startedAtMs,
		RecentLogs:   logs,
	}
}

// AutoRestart reports whether automatic restart on game exit is enabled.
func (s *Supervisor) AutoRestart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoRestart
}

// SetAutoRestart updates whether the supervisor automatically restarts the game after exit/crash.
func (s *Supervisor) SetAutoRestart(enabled bool) {
	s.mu.Lock()
	s.autoRestart = enabled
	s.mu.Unlock()
}

// Restart terminates the running game binary (if any) and launches a fresh instance.
func (s *Supervisor) Restart() {
	s.mu.Lock()
	s.stopRequested = false
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case s.restartCh <- struct{}{}:
	default:
	}
}

// Stop stops the running game binary and suppresses auto-restart until Restart() is called.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.stopRequested = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Run supervises the child game process until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	if strings.TrimSpace(s.binary) == "" {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		stopped := s.stopRequested
		s.mu.Unlock()
		if stopped {
			select {
			case <-ctx.Done():
				return
			case <-s.restartCh:
				continue
			}
		}

		exitCode, uptime := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		s.mu.Lock()
		stopped = s.stopRequested
		autoRestart := s.autoRestart
		s.lastExitCode = exitCode
		logsTail := append([]string(nil), s.recentLogs...)
		restarts := s.restartCount
		s.mu.Unlock()

		if s.outbox != nil {
			kind := "launcher.game_exited"
			sev := "info"
			if exitCode != 0 && !stopped {
				kind = "launcher.game_crashed"
				sev = "error"
			}
			_, _ = s.outbox.Record(context.Background(), cktelemetry.Event{
				Category: "launcher",
				Kind:     kind,
				Severity: sev,
				Payload: map[string]any{
					"binary":        s.binary,
					"exit_code":     exitCode,
					"uptime_s":      int(uptime.Seconds()),
					"restart_count": restarts,
					"logs_tail":     logsTail,
				},
			})
		}

		if stopped || !autoRestart {
			select {
			case <-ctx.Done():
				return
			case <-s.restartCh:
				continue
			}
		}

		s.mu.Lock()
		s.restartCount++
		s.mu.Unlock()

		delay := 2 * time.Second
		if uptime < 5*time.Second {
			delay = 5 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.restartCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) (int, time.Duration) {
	args := append([]string(nil), s.extraArgs...)
	args = append(args,
		"--server-url="+s.serverURL,
		"--bridge-token="+s.bridgeToken,
	)
	cmd := exec.CommandContext(ctx, s.binary, args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		s.appendLog(fmt.Sprintf("failed to launch %s: %v", s.binary, err))
		return -1, 0
	}

	s.mu.Lock()
	s.cmd = cmd
	s.running = true
	s.pid = cmd.Process.Pid
	s.startedAtMs = start.UnixMilli()
	s.mu.Unlock()

	if s.outbox != nil {
		_, _ = s.outbox.Record(context.Background(), cktelemetry.Event{
			Category: "launcher",
			Kind:     "launcher.game_started",
			Severity: "info",
			Payload: map[string]any{
				"binary": s.binary,
				"pid":    cmd.Process.Pid,
			},
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.drainPipe("stdout", stdout) }()
	go func() { defer wg.Done(); s.drainPipe("stderr", stderr) }()

	err := cmd.Wait()
	wg.Wait()

	s.mu.Lock()
	s.running = false
	s.pid = 0
	s.cmd = nil
	s.mu.Unlock()

	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return exitCode, time.Since(start)
}

func (s *Supervisor) drainPipe(label string, r io.ReadCloser) {
	if r == nil {
		return
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		s.appendLog("[" + label + "] " + sc.Text())
	}
}

func (s *Supervisor) appendLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const maxLines = 40
	s.recentLogs = append(s.recentLogs, line)
	if len(s.recentLogs) > maxLines {
		s.recentLogs = s.recentLogs[len(s.recentLogs)-maxLines:]
	}
}
