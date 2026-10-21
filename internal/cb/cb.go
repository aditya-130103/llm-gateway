package cb

import (
	"sync"
	"time"
)

type state int

const (
	closed   state = iota
	open
	halfOpen
)

type CB struct {
	state            state
	lastFailureTime  time.Time
	failureCount     int
	failureThreshold int
	cooldownPeriod   time.Duration
	probeInFlight    bool
	lock             sync.Mutex
}

func NewCB(failureThreshold int, cooldownPeriod time.Duration) *CB {
	return &CB{
		state:            closed,
		failureThreshold: failureThreshold,
		cooldownPeriod:   cooldownPeriod,
	}
}

func (cb *CB) Allow() bool {
	cb.lock.Lock()
	defer cb.lock.Unlock()

	if cb.state == open {
		if time.Since(cb.lastFailureTime) > cb.cooldownPeriod {
			cb.state = halfOpen
			cb.probeInFlight = true
			return true
		}
		return false
	}

	if cb.state == halfOpen {
		// Only one probe at a time — reject concurrent requests during recovery.
		if cb.probeInFlight {
			return false
		}
	}

	return true
}

func (cb *CB) RecordFailure() {
	cb.lock.Lock()
	defer cb.lock.Unlock()

	if cb.state == halfOpen {
		cb.state = open
		cb.probeInFlight = false
	} else {
		cb.failureCount++
		if cb.failureCount >= cb.failureThreshold {
			cb.state = open
		}
	}
	cb.lastFailureTime = time.Now()
}

func (cb *CB) RecordSuccess() {
	cb.lock.Lock()
	defer cb.lock.Unlock()

	cb.failureCount = 0
	cb.state = closed
	cb.probeInFlight = false
}
