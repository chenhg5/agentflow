package domain

import "time"

// Machine is a runner that has reported to the Hub.
type Machine struct {
	ID        string
	Hostname  string
	Version   string
	FirstSeen time.Time
	LastSeen  time.Time
	// Capabilities are what the runner last said it lets the Hub ask of it.
	Capabilities []string
}

// Accepts reports whether the machine said it handles capability.
func (m Machine) Accepts(capability string) bool {
	for _, item := range m.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

// Seen updates a machine with a new report.
func (m Machine) Seen(hostname, version string, capabilities []string, at time.Time) Machine {
	if m.FirstSeen.IsZero() || at.Before(m.FirstSeen) {
		m.FirstSeen = at
	}
	if at.After(m.LastSeen) {
		m.LastSeen = at
		if hostname != "" {
			m.Hostname = hostname
		}
		if version != "" {
			m.Version = version
		}
		m.Capabilities = append([]string(nil), capabilities...)
	}
	return m
}
