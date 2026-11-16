package cron

import "time"

// alertVerdict is what one check tells Telegram.
type alertVerdict int

const (
	// alertQuiet sends nothing.
	alertQuiet alertVerdict = iota
	// alertRaise announces a new alert.
	alertRaise
	// alertRemind repeats an alert that is still raised.
	alertRemind
	// alertClear announces that the alert is over.
	alertClear
)

// alertFlag is the raised-or-not part of an alert that every kind of alert
// keeps in the status file: whether it is raised and when it was last announced.
type alertFlag struct {
	On bool
	At time.Time
}

// decideAlert is the one rule behind every alert: raise, remind, clear.
// prev is the flag after the last check. hot says the trouble is present now and
// calm says it is over; both false keeps the flag as it is, which is how a
// reading between the alert and recover thresholds behaves. repeat is the
// reminder period while hot; 0 never reminds. now is the time of the check.
// It returns the new flag and the verdict: raise when hot and not yet raised
// (At is now), remind when hot and repeat has passed since At (At moves to now),
// clear when raised and calm (the flag is reset), quiet otherwise.
// It does not fail.
func decideAlert(prev alertFlag, hot, calm bool, repeat time.Duration, now time.Time) (alertFlag, alertVerdict) {
	next := prev
	switch {
	case hot && !next.On:
		return alertFlag{On: true, At: now}, alertRaise
	case hot && repeat > 0 && now.Sub(next.At) >= repeat:
		next.At = now
		return next, alertRemind
	case next.On && calm:
		return alertFlag{}, alertClear
	}
	return next, alertQuiet
}

// launchCheck starts fn in its own goroutine when the check behind next and busy
// is due and none of its runs is in flight. next is the time of the next run and
// busy says a run is in flight; both belong to the caller and live under r.mu.
// every is the pause after a start. A run that panics is not caught here.
func (r *Runner) launchCheck(next *time.Time, busy *bool, every time.Duration, fn func()) {
	now := r.now()
	r.mu.Lock()
	if *busy || now.Before(*next) {
		r.mu.Unlock()
		return
	}
	*busy, *next = true, now.Add(every)
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fn()
		r.mu.Lock()
		*busy = false
		r.mu.Unlock()
	}()
}
