// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

const progressBarWidth = 28
const visibleItems = 8

// EventType describes one operation lifecycle transition.
type EventType string

const (
	// Status reports the current operation phase.
	Status EventType = "status"
	// Checking marks an item whose cache is being checked.
	Checking EventType = "checking"
	// Cached marks an item already available locally.
	Cached EventType = "cached"
	// Queued marks an item waiting to transfer.
	Queued EventType = "queued"
	// Started marks an active transfer.
	Started EventType = "started"
	// Progressed updates an active transfer's byte count.
	Progressed EventType = "progress"
	// Done marks a completed transfer.
	Done EventType = "done"
	// Failed marks a failed transfer.
	Failed EventType = "failed"
)

// Event reports progress for one item.
type Event struct {
	Type    EventType
	Name    string
	Message string
	Current int64
	Total   int64
	Err     error
}

// Progress receives operation events.
type Progress func(Event)

// Run renders progress while run executes.
func Run(output io.Writer, title string, run func(Progress) error) error {
	file, interactive := output.(*os.File)
	if !interactive || !term.IsTerminal(int(file.Fd())) || strings.EqualFold(os.Getenv("CI"), "true") || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return runText(output, run)
	}
	events := make(chan Event, 256)
	m := model{title: title, run: run, events: events, items: map[string]item{}}
	final, err := tea.NewProgram(m, tea.WithOutput(output)).Run()
	if err != nil {
		return fmt.Errorf("render progress: %w", err)
	}
	if result, ok := final.(model); ok {
		return result.err
	}
	return nil
}

// item is one rendered progress row.
type item struct {
	Name    string
	Status  EventType
	Current int64
	Total   int64
	Err     error
}

// model is the Bubble Tea progress dashboard.
type model struct {
	title        string
	run          func(Progress) error
	events       chan Event
	items        map[string]item
	order        []string
	message      string
	err          error
	doneReceived bool
	closed       bool
}

// eventMsg delivers one progress event to Bubble Tea.
type eventMsg Event

// doneMsg reports completion of the operation.
type doneMsg struct{ err error }

// closedMsg reports that no more progress events remain.
type closedMsg struct{}

// Init starts the operation and event reader.
func (m model) Init() tea.Cmd { return tea.Batch(m.runCommand(), m.waitForEvent()) }

// runCommand executes the operation in the background.
func (m model) runCommand() tea.Cmd {
	return func() tea.Msg {
		err := m.run(func(event Event) {
			select {
			case m.events <- event:
			default:
			}
		})
		close(m.events)
		return doneMsg{err: err}
	}
}

// waitForEvent waits for one progress event.
func (m model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.events
		if !ok {
			return closedMsg{}
		}
		return eventMsg(event)
	}
}

// Update applies progress events and exits after work and events complete.
func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyMsg:
		if message.String() == "ctrl+c" || message.String() == "q" {
			m.err = context.Canceled
			return m, tea.Quit
		}
	case eventMsg:
		event := Event(message)
		if event.Type == Status {
			m.message = event.Message
			return m, m.waitForEvent()
		}
		current, exists := m.items[event.Name]
		if !exists {
			current.Name = event.Name
			m.order = append(m.order, event.Name)
		}
		current.Status = event.Type
		current.Current = event.Current
		if event.Total > 0 {
			current.Total = event.Total
		}
		current.Err = event.Err
		if event.Type == Done || event.Type == Cached {
			if current.Total == 0 {
				current.Total = current.Current
			}
			if current.Current == 0 {
				current.Current = current.Total
			}
		}
		m.items[event.Name] = current
		return m, m.waitForEvent()
	case doneMsg:
		m.doneReceived = true
		m.err = message.err
		if m.closed {
			return m, tea.Quit
		}
	case closedMsg:
		m.closed = true
		if m.doneReceived {
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders overall and per-item progress.
func (m model) View() string {
	var body strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00d7af")).Render(m.title)
	current, total := m.overall()
	fmt.Fprintf(&body, "\n%s\n\n", title)
	if m.message != "" {
		fmt.Fprintf(&body, "%s\n\n", m.message)
	}
	fmt.Fprintf(&body, "Overall  %s  %s\n\n", bar(current, total, progressBarWidth), bytesProgress(current, total))
	start := 0
	if len(m.order) > visibleItems {
		start = len(m.order) - visibleItems
	}
	for _, name := range m.order[start:] {
		entry := m.items[name]
		status := "•"
		switch entry.Status {
		case Started, Progressed:
			status = "⟳"
		case Done, Cached:
			status = "✓"
		case Failed:
			status = "✗"
		}
		if entry.Err != nil {
			fmt.Fprintf(&body, "%s %-24s %v\n", status, truncate(entry.Name, 24), entry.Err)
		} else if entry.Status == Started || entry.Status == Progressed {
			fmt.Fprintf(&body, "%s %-24s %s %s\n", status, truncate(entry.Name, 24), bar(entry.Current, entry.Total, 14), bytesProgress(entry.Current, entry.Total))
		} else if entry.Total > 0 {
			fmt.Fprintf(&body, "%s %-24s %s\n", status, truncate(entry.Name, 24), formatBytes(entry.Total))
		} else {
			fmt.Fprintf(&body, "%s %-24s %s\n", status, truncate(entry.Name, 24), entry.Status)
		}
	}
	if hidden := len(m.order) - (len(m.order) - start); hidden > 0 {
		fmt.Fprintf(&body, "… %d earlier files hidden\n", hidden)
	}
	complete := 0
	for _, entry := range m.items {
		if entry.Status == Done || entry.Status == Cached {
			complete++
		}
	}
	fmt.Fprintf(&body, "\n%d files, %d complete\n", len(m.items), complete)
	return body.String()
}

// overall sums known item sizes.
func (m model) overall() (int64, int64) {
	var current, total int64
	for _, entry := range m.items {
		if entry.Total <= 0 {
			continue
		}
		total += entry.Total
		current += min(entry.Current, entry.Total)
	}
	return current, total
}

// runText reports lifecycle events when output is not an interactive terminal.
func runText(output io.Writer, run func(Progress) error) error {
	var mu sync.Mutex
	return run(func(event Event) {
		mu.Lock()
		defer mu.Unlock()
		switch event.Type {
		case Status:
			if event.Message != "" {
				_, _ = fmt.Fprintln(output, event.Message)
			}
		case Checking:
			_, _ = fmt.Fprintf(output, "Checking %s (%s)...\n", event.Name, formatBytes(event.Total))
		case Started:
			_, _ = fmt.Fprintf(output, "Transferring %s (%s)...\n", event.Name, formatBytes(event.Total))
		case Done:
			_, _ = fmt.Fprintf(output, "Transferred %s (%s).\n", event.Name, formatBytes(event.Current))
		case Cached:
			_, _ = fmt.Fprintf(output, "Using cached %s (%s).\n", event.Name, formatBytes(event.Total))
		case Failed:
			_, _ = fmt.Fprintf(output, "Failed %s: %v\n", event.Name, event.Err)
		}
	})
}

// truncate shortens value to width runes for stable rows.
func truncate(value string, width int) string {
	chars := []rune(value)
	if len(chars) <= width {
		return value
	}
	return string(chars[:width-1]) + "…"
}

// bar renders a fixed-width byte progress bar.
func bar(current, total int64, width int) string {
	filled := 0
	if total > 0 {
		filled = int(float64(current) / float64(total) * float64(width))
		filled = min(max(filled, 0), width)
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// bytesProgress renders current and total byte counts.
func bytesProgress(current, total int64) string {
	if total <= 0 {
		return formatBytes(current)
	}
	return fmt.Sprintf("%s / %s", formatBytes(current), formatBytes(total))
}

// formatBytes renders bytes using binary units.
func formatBytes(value int64) string {
	if value < 0 {
		return "unknown size"
	}
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
