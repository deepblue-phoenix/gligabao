package agent_research

import (
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	log "github.com/sirupsen/logrus"
)

// scheduler starts the researches of the agent definitions that have a [schedule] (agent_defs.Schedule): at every time the CRON
// expression matches it starts a research of the schedule's topic, for the schedule's user, with that agent. It keeps nothing in the
// database: after a restart the next time after the start is used, so the times the server was down are not caught up, and a run that
// would start while the previous scheduled run of the same agent still works is skipped.
type scheduler struct {
	m       *Manager
	entries []*scheduled
}

// scheduled is an agent with a schedule that is not paused.
type scheduled struct {
	def  *agent_defs.Def
	next time.Time // the next time it is due (zero: never again)

	mu      sync.Mutex
	lastRun int64 // the ID of the research its last due time started (0: none)
}

// maxScheduleSleep is the longest the scheduler sleeps without looking at the clock again (a clock that was set while it slept).
const maxScheduleSleep = time.Minute

// newScheduler makes the scheduler of the definitions of m, with the first due time of each after from.
func newScheduler(m *Manager, from time.Time) *scheduler {
	s := &scheduler{
		m: m,
	}
	for _, d := range m.registry().List() {
		if d.Schedule == nil || d.Schedule.Paused {
			continue
		}
		e := &scheduled{
			def:  d,
			next: d.Schedule.Next(from),
		}
		s.entries = append(s.entries, e)
		log.WithFields(log.Fields{"agent": d.Name, "cron": d.Schedule.Cron, "timezone": d.Schedule.Timezone, "user": d.Schedule.User, "next": e.next}).Info("agent research: scheduled agent")
	}
	return s
}

// tick starts the researches that are due at now (each agent at most once, even if several of its times have passed) and moves each
// agent on to its next time after now.
func (s *scheduler) tick(now time.Time) {
	for _, e := range s.entries {
		if e.next.IsZero() || e.next.After(now) {
			continue
		}
		due := e.next
		e.next = e.def.Schedule.Next(now)
		s.fire(e, due)
	}
}

// fire starts the research of e that was due at due, unless the previous one is still working.
func (s *scheduler) fire(e *scheduled, due time.Time) {
	sc := e.def.Schedule
	l := log.WithFields(log.Fields{"agent": e.def.Name, "user_id": sc.User, "due": due})
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastRun != 0 {
		if prev, err := s.m.Get(s.m.ctx, sc.User, e.lastRun); err == nil && prev.Status.Active() {
			l.WithField("run_id", e.lastRun).Warn("agent research: scheduled run skipped, the previous one is still working")
			return
		}
	}
	topic, err := sc.TopicAt(due)
	if err != nil {
		l.WithError(err).Error("agent research: scheduled run skipped, the topic cannot be made")
		return
	}
	run, err := s.m.Start(s.m.ctx, sc.User, Options{
		Topic: topic,
		Agent: e.def.Name,
	})
	if err != nil {
		l.WithError(err).Error("agent research: scheduled run could not be started")
		return
	}
	e.lastRun = run.ID
	l.WithField("run_id", run.ID).Info("agent research: scheduled run started")
}

// next is the earliest due time of all the agents (zero if none).
func (s *scheduler) nextDue() time.Time {
	var first time.Time
	for _, e := range s.entries {
		if !e.next.IsZero() && (first.IsZero() || e.next.Before(first)) {
			first = e.next
		}
	}
	return first
}

// loop runs until the manager's context is done.
func (s *scheduler) loop() {
	for {
		wait := maxScheduleSleep
		if due := s.nextDue(); !due.IsZero() {
			wait = min(max(time.Until(due), 0), maxScheduleSleep)
		}
		t := time.NewTimer(wait)
		select {
		case <-s.m.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.tick(time.Now())
	}
}

// RunSchedules starts the schedules of the agent definitions (agent_defs.Def.Schedule) that are not paused, in the background until the
// manager's context is done, and returns how many agents are scheduled. Call it once, after UseAgentDefs and the other settings, and
// only in the one process that should run them (every process that calls it starts its own runs). Without an LLM nothing is started.
func (m *Manager) RunSchedules() int {
	s := newScheduler(m, time.Now())
	if len(s.entries) == 0 {
		return 0
	}
	if m.Client == nil {
		log.Warn("agent research: agents have schedules but no LLM is available (set ANTHROPIC_API_KEY): they will not run")
		return 0
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		s.loop()
	}()
	return len(s.entries)
}
