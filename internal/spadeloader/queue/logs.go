package queue

import "strings"

const (
	defaultConsoleTailLines = 200
	maxConsoleTailLines     = 5000
)

func (m *Manager) ReadConsoleTail(jobID string, lines int) ([]byte, error) {
	raw, err := m.ReadConsoleLog(jobID)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = defaultConsoleTailLines
	}
	if lines > maxConsoleTailLines {
		lines = maxConsoleTailLines
	}
	return tailLastLines(raw, lines), nil
}

func tailLastLines(raw []byte, lines int) []byte {
	if lines <= 0 {
		return raw
	}
	s := string(raw)
	parts := strings.Split(s, "\n")
	if len(parts) == 0 {
		return raw
	}
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= lines {
		if len(parts) == 0 {
			return nil
		}
		return []byte(strings.Join(parts, "\n") + "\n")
	}
	start := len(parts) - lines
	return []byte(strings.Join(parts[start:], "\n") + "\n")
}
