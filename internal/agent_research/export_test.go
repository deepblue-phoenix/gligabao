package agent_research

import "time"

// SetExportRetryDelay shortens the wait between the attempts to download an export; it returns the function that restores it.
func SetExportRetryDelay(d time.Duration) (restore func()) {
	old := remoteExportRetryDelay
	remoteExportRetryDelay = d
	return func() { remoteExportRetryDelay = old }
}

// ScheduleTicker makes the scheduler of m with its first due times after from and returns its tick, to call with the time it is now.
func ScheduleTicker(m *Manager, from time.Time) func(now time.Time) {
	return newScheduler(m, from).tick
}
