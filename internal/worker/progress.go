package worker

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Progress tracks and displays tile generation progress.
type Progress struct {
	startTime time.Time
	output    io.Writer
	total     int
	completed int
	failed    int
	mu        sync.RWMutex
	enabled   bool
}

// NewProgress creates a new progress tracker.
func NewProgress(total int, enabled bool) *Progress {
	return &Progress{
		total:     total,
		startTime: time.Now(),
		output:    os.Stderr,
		enabled:   enabled,
	}
}

// Update records the completion of a task.
func (p *Progress) Update(completed, total, failed int) {
	p.mu.Lock()
	p.completed = completed
	p.total = total
	p.failed = failed
	p.mu.Unlock()

	if p.enabled {
		p.Print()
	}
}

// Callback returns a ProgressFunc suitable for use with Pool.Config.
func (p *Progress) Callback() ProgressFunc {
	return p.Update
}

// Print displays the current progress to output.
func (p *Progress) Print() {
	p.mu.RLock()
	completed := p.completed
	total := p.total
	failed := p.failed
	startTime := p.startTime
	p.mu.RUnlock()

	elapsed := time.Since(startTime)

	// Calculate rate and ETA
	var rate float64
	var eta time.Duration
	if completed > 0 {
		rate = float64(completed) / elapsed.Seconds()
		remaining := total - completed
		if rate > 0 {
			eta = time.Duration(float64(remaining)/rate) * time.Second
		}
	}

	// Build progress bar
	barWidth := 30
	progress := float64(completed) / float64(total)
	filledWidth := int(progress * float64(barWidth))
	bar := strings.Repeat("█", filledWidth) + strings.Repeat("░", barWidth-filledWidth)

	// Format output
	line := fmt.Sprintf("\r[%s] %d/%d tiles", bar, completed, total)
	if failed > 0 {
		line += fmt.Sprintf(" (%d failed)", failed)
	}
	line += fmt.Sprintf(" - %.1f tiles/sec", rate)
	if eta > 0 && completed < total {
		line += fmt.Sprintf(" - ETA: %s", formatDuration(eta))
	}
	if completed == total {
		line += fmt.Sprintf(" - Done in %s", formatDuration(elapsed))
	}

	// Pad to clear previous line content
	line += "          "

	writeProgress(p.output, line)
}

// writeProgress emits progress output, dropping write errors on purpose.
// Progress is cosmetic, Print and Done have no error path to report on, and a
// broken output stream must not derail tile generation.
func writeProgress(w io.Writer, s string) {
	//nolint:errcheck // cosmetic output; a failed write is deliberately ignored
	fmt.Fprint(w, s)
}

// Done prints the final progress and a newline.
func (p *Progress) Done() {
	if p.enabled {
		p.Print()
		writeProgress(p.output, "\n")
	}
}

// Summary returns a summary string of the completed work.
func (p *Progress) Summary() string {
	p.mu.RLock()
	completed := p.completed
	total := p.total
	failed := p.failed
	startTime := p.startTime
	p.mu.RUnlock()

	elapsed := time.Since(startTime)
	successful := completed - failed

	var rate float64
	if elapsed.Seconds() > 0 {
		rate = float64(completed) / elapsed.Seconds()
	}

	return fmt.Sprintf("Generated %d/%d tiles (%d failed) in %s (%.1f tiles/sec)",
		successful, total, failed, formatDuration(elapsed), rate)
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", mins, secs)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}
