package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mblsha/spadeforge/internal/spadeloader/client"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

const (
	defaultLimit           = 100
	defaultRefreshInterval = 1500 * time.Millisecond
	defaultReflashTimeout  = 30 * time.Second
)

type Options struct {
	Client          *client.HTTPClient
	Limit           int
	RefreshInterval time.Duration
	ReflashTimeout  time.Duration
}

func Run(ctx context.Context, opts Options) error {
	model, err := newModel(opts)
	if err != nil {
		return err
	}
	p := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

type refreshTickMsg struct{}

type jobsLoadedMsg struct {
	items []job.Record
	err   error
}

type reflashResultMsg struct {
	newJobID string
	err      error
}

type model struct {
	client *client.HTTPClient

	limit           int
	refreshInterval time.Duration
	reflashTimeout  time.Duration

	items []job.Record

	selectedIdx int
	selectedID  string
	pendingID   string

	width  int
	height int

	loading    bool
	reflashing bool
	status     string
	lastErr    string
}

func newModel(opts Options) (model, error) {
	if opts.Client == nil {
		return model{}, fmt.Errorf("tui client is required")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	refresh := opts.RefreshInterval
	if refresh <= 0 {
		refresh = defaultRefreshInterval
	}
	reflashTimeout := opts.ReflashTimeout
	if reflashTimeout <= 0 {
		reflashTimeout = defaultReflashTimeout
	}
	return model{
		client:          opts.Client,
		limit:           limit,
		refreshInterval: refresh,
		reflashTimeout:  reflashTimeout,
		loading:         true,
		status:          "loading bitstreams...",
	}, nil
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchJobsCmd(), m.tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		return m, nil
	case refreshTickMsg:
		m.loading = true
		return m, tea.Batch(m.fetchJobsCmd(), m.tickCmd())
	case jobsLoadedMsg:
		m.loading = false
		if typed.err != nil {
			m.lastErr = typed.err.Error()
			m.status = "refresh failed"
			return m, nil
		}
		m.lastErr = ""
		m.applyJobs(typed.items)
		if m.reflashing {
			m.status = "submitting reflash..."
		} else {
			m.status = fmt.Sprintf("loaded %d bitstream entries", len(m.items))
		}
		return m, nil
	case reflashResultMsg:
		m.reflashing = false
		if typed.err != nil {
			m.lastErr = typed.err.Error()
			m.status = "reflash failed"
			return m, nil
		}
		m.lastErr = ""
		m.pendingID = typed.newJobID
		m.status = fmt.Sprintf("reflash submitted: %s", typed.newJobID)
		m.loading = true
		return m, m.fetchJobsCmd()
	case tea.KeyMsg:
		switch typed.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "k", "up":
			m.moveSelection(-1)
			return m, nil
		case "j", "down":
			m.moveSelection(1)
			return m, nil
		case "r":
			m.loading = true
			return m, m.fetchJobsCmd()
		case "enter":
			if m.reflashing {
				return m, nil
			}
			selected, ok := m.selected()
			if !ok {
				return m, nil
			}
			m.reflashing = true
			m.status = fmt.Sprintf("reflashing %s | %s ...", selected.Board, selected.DesignName)
			m.lastErr = ""
			return m, m.reflashCmd(selected.ID)
		}
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString("Spadeloader TUI - Bitstreams (newest first)\n")
	b.WriteString("Keys: j/k or arrows move  enter reflash  r refresh  q quit\n")
	b.WriteString(m.statusLine())
	b.WriteString("\n")
	if len(m.items) == 0 {
		if m.loading {
			b.WriteString("\nLoading...\n")
		} else {
			b.WriteString("\nNo bitstreams yet.\n")
		}
		return b.String()
	}

	rows := m.visibleRows()
	for i := rows.start; i < rows.end; i++ {
		rec := m.items[i]
		prefix := "  "
		if i == m.selectedIdx {
			prefix = "> "
		}
		created := rec.CreatedAt.Local().Format("2006-01-02 15:04:05")
		line := fmt.Sprintf(
			"%s%s  %-12s  %-24s  %-10s  %s",
			prefix,
			created,
			rec.Board,
			rec.DesignName,
			rec.State,
			shortID(rec.ID),
		)
		b.WriteString(trimToWidth(line, m.width))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) applyJobs(items []job.Record) {
	sorted := append([]job.Record(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID > sorted[j].ID
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	unique := make([]job.Record, 0, len(sorted))
	seen := make(map[string]struct{}, len(sorted))
	for _, rec := range sorted {
		key := bitstreamKey(rec)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, rec)
	}
	m.items = unique
	if len(m.items) == 0 {
		m.selectedIdx = 0
		m.selectedID = ""
		m.pendingID = ""
		return
	}

	targetID := m.pendingID
	if strings.TrimSpace(targetID) == "" {
		targetID = m.selectedID
	}
	m.pendingID = ""

	if strings.TrimSpace(targetID) != "" {
		for i := range m.items {
			if m.items[i].ID == targetID {
				m.selectedIdx = i
				m.selectedID = targetID
				return
			}
		}
	}
	m.selectedIdx = 0
	m.selectedID = m.items[0].ID
}

func (m *model) moveSelection(delta int) {
	if len(m.items) == 0 || delta == 0 {
		return
	}
	next := m.selectedIdx + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.items) {
		next = len(m.items) - 1
	}
	m.selectedIdx = next
	m.selectedID = m.items[next].ID
}

func (m model) selected() (job.Record, bool) {
	if len(m.items) == 0 || m.selectedIdx < 0 || m.selectedIdx >= len(m.items) {
		return job.Record{}, false
	}
	return m.items[m.selectedIdx], true
}

type rowWindow struct {
	start int
	end   int
}

func (m model) visibleRows() rowWindow {
	maxRows := m.height - 5
	if maxRows <= 0 {
		maxRows = len(m.items)
	}
	if maxRows > len(m.items) {
		maxRows = len(m.items)
	}
	start := 0
	if m.selectedIdx >= maxRows {
		start = m.selectedIdx - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.items) {
		end = len(m.items)
	}
	return rowWindow{start: start, end: end}
}

func (m model) statusLine() string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(m.status) != "" {
		parts = append(parts, m.status)
	}
	if strings.TrimSpace(m.lastErr) != "" {
		parts = append(parts, "error: "+m.lastErr)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

func (m model) fetchJobsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.refreshInterval)
		defer cancel()
		items, err := m.client.ListJobs(ctx, m.limit)
		return jobsLoadedMsg{items: items, err: err}
	}
}

func (m model) tickCmd() tea.Cmd {
	return tea.Tick(m.refreshInterval, func(_ time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func (m model) reflashCmd(sourceJobID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), m.reflashTimeout)
		defer cancel()
		newID, err := m.client.ReflashJob(ctx, sourceJobID)
		return reflashResultMsg{newJobID: newID, err: err}
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func bitstreamKey(rec job.Record) string {
	board := strings.TrimSpace(rec.Board)
	design := strings.TrimSpace(rec.DesignName)
	sha := strings.TrimSpace(rec.BitstreamSHA256)
	name := strings.TrimSpace(rec.BitstreamName)
	if board == "" && design == "" && sha == "" && name == "" {
		return strings.TrimSpace(rec.ID)
	}

	var b strings.Builder
	b.Grow(len(rec.Board) + len(rec.DesignName) + len(rec.BitstreamSHA256) + len(rec.BitstreamName) + 4)
	b.WriteString(board)
	b.WriteByte('|')
	b.WriteString(design)
	b.WriteByte('|')
	b.WriteString(sha)
	b.WriteByte('|')
	b.WriteString(name)
	return b.String()
}

func trimToWidth(in string, width int) string {
	if width <= 0 {
		return in
	}
	if len(in) <= width {
		return in
	}
	if width <= 3 {
		return in[:width]
	}
	return in[:width-3] + "..."
}
