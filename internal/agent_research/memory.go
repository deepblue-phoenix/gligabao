package agent_research

import (
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_memory"
)

// Memory is how the research agents reach the internal knowledge base (see package agent_memory).
type Memory = agent_memory.Memory

// SessionPrefix starts the session ID of the remember and query calls of an agent; SessionID is the whole of it.
const SessionPrefix = "research-"

// SessionID is the session of the calls the agent of run runID makes: everything it remembers (the artifacts, entities and facts) and
// every query it asks carries it, so that what an agent contributed can be told (see the provenance of the admin UI).
func SessionID(runID int64) string {
	return fmt.Sprintf("%s%d",
		SessionPrefix,
		runID)
}
