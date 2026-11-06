package domain

import "time"

// Machine is a runner that has reported to the Hub.
type Machine struct {
	ID        string
	Hostname  string
	Version   string
	FirstSeen time.Time
	LastSeen  time.Time
}

// Seen updates a machine with a new report.
func (m Machine) Seen(hostname, version string, at time.Time) Machine {
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
	}
	return m
}
