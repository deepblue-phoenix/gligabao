package agent_research

import "time"

// SetExportRetryDelay shortens the wait between the attempts to download an export; it returns the function that restores it.
func SetExportRetryDelay(d time.Duration) (restore func()) {
	old := remoteExportRetryDelay
	remoteExportRetryDelay = d
	return func() { remoteExportRetryDelay = old }
}
