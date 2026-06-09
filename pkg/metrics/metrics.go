package metrics

import (
	"time"
)

type EventTracker struct {
	counter  uint64
	lastTime time.Time
}

func NewEventTracker() *EventTracker {
	return &EventTracker{
		lastTime: time.Now(),
	}
}

func (m *EventTracker) Inc() {
	m.counter += 1
}

func (m *EventTracker) Rate() float64 {
	currentTtime := time.Now()
	currentValue := m.counter
	elapsedSeconds := currentTtime.Sub(m.lastTime).Seconds()
	if elapsedSeconds <= 0 {
		return 0.0
	}

	rate := float64(currentValue) / elapsedSeconds
	m.counter = 0
	m.lastTime = currentTtime
	return rate
}
