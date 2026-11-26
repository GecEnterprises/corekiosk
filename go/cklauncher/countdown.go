package cklauncher

import (
	"context"
	"sync"
	"time"
)

// CountdownState represents the pre-launch booth countdown shown in cklauncher UI.
type CountdownState struct {
	TotalSeconds     int  `json:"total_seconds"`
	RemainingSeconds int  `json:"remaining_seconds"`
	Paused           bool `json:"paused"`
	Launched         bool `json:"launched"`
	Disabled         bool `json:"disabled"`
}

// Countdown manages the pre-launch countdown before fullscreen game takeover.
type Countdown struct {
	mu       sync.Mutex
	total    int
	rem      int
	paused   bool
	launched bool
	disabled bool
	launchCh chan struct{}
}

func NewCountdown(seconds int, disabled bool) *Countdown {
	if seconds < 0 {
		seconds = 0
	}
	return &Countdown{
		total:    seconds,
		rem:      seconds,
		disabled: disabled,
		launchCh: make(chan struct{}, 1),
	}
}

func (c *Countdown) State() CountdownState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CountdownState{
		TotalSeconds:     c.total,
		RemainingSeconds: c.rem,
		Paused:           c.paused,
		Launched:         c.launched,
		Disabled:         c.disabled,
	}
}

func (c *Countdown) Pause() {
	c.mu.Lock()
	c.paused = true
	c.mu.Unlock()
}

func (c *Countdown) Resume() {
	c.mu.Lock()
	c.paused = false
	c.mu.Unlock()
}

func (c *Countdown) LaunchNow() {
	c.mu.Lock()
	c.rem = 0
	c.paused = false
	c.mu.Unlock()
	select {
	case c.launchCh <- struct{}{}:
	default:
	}
}

// Wait blocks until the countdown reaches 0, LaunchNow is called, or ctx is cancelled.
// Returns true if game launch should proceed.
func (c *Countdown) Wait(ctx context.Context) bool {
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return false
	}
	if c.rem <= 0 {
		c.launched = true
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-c.launchCh:
			c.mu.Lock()
			c.launched = true
			c.rem = 0
			c.mu.Unlock()
			return true
		case <-ticker.C:
			c.mu.Lock()
			if !c.paused && c.rem > 0 {
				c.rem--
			}
			done := c.rem <= 0
			if done {
				c.launched = true
			}
			c.mu.Unlock()
			if done {
				return true
			}
		}
	}
}
