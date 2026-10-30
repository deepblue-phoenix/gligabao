package agent_research

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/core"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/db"
	log "github.com/sirupsen/logrus"
)

// Limits of the sub-agents. They bound what one research can cost: a sub-agent works like any other agent, with up to dozens of
// paid model calls. Sub-agents are not counted in the DefaultMaxActivePerUser of the user's researches; these limits bound them.
const (
	// MaxSubagentDepth is how many generations of sub-agents a research may have: its sub-agents may spawn their own, whose agents
	// may not spawn any more (with 2).
	MaxSubagentDepth = 2
	// MaxSubagentsPerAgent is the most sub-agents one agent spawns, and MaxSubagentsPerTree the most in the whole tree of a research.
	MaxSubagentsPerAgent = 5
	MaxSubagentsPerTree  = 10
	// DefaultSubagentMaxSteps is the number of model calls a sub-agent may make when its parent says none.
	DefaultSubagentMaxSteps = 10
)

// outcome is how a run ended.
type outcome struct {
	status core.Status
	report string
	err    string
}

// member is a sub-agent as its parent and its siblings know it. Only id, name and reason (which never change) are read before the
// sub-agent has ended; out is written before done is closed, and read after.
type member struct {
	id     int64
	name   string
	reason string
	done   chan struct{}
	out    outcome
}

// ended reports whether the sub-agent has ended.
func (m *member) ended() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

// family is the sub-agents of one parent, in the order they were spawned. The siblings of an agent are the other members of the family
// of its parent: that is the only way an agent finds another, so it can reach no one else.
type family struct {
	mu      sync.Mutex
	members []*member
}

func (f *family) get(name string) *member {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.members {
		if m.name == name {
			return m
		}
	}
	return nil
}

func (f *family) all() []*member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*member(nil), f.members...)
}

func (f *family) names(except string) string {
	var names []string
	for _, m := range f.all() {
		if m.name != except {
			names = append(names, m.name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// waitAll blocks until all the members have ended.
func (f *family) waitAll() {
	for _, m := range f.all() {
		<-m.done
	}
}

// tree counts the sub-agents of one research (the root and everything below it).
type tree struct {
	mu      sync.Mutex
	spawned int
}

func (t *tree) reserve() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.spawned >= MaxSubagentsPerTree {
		return false
	}
	t.spawned++
	return true
}

func (t *tree) release() {
	t.mu.Lock()
	t.spawned--
	t.mu.Unlock()
}

// team is the view of one agent of the sub-agents and the messages (agent.Team).
type team struct{ r *researcher }

var _ agent.Team = team{}

// Spawn starts a sub-agent of the agent.
func (t team) Spawn(_ context.Context, name, reason, task string, maxSteps int) (string, error) {
	r := t.r
	if r.depth >= MaxSubagentDepth {
		return "", fmt.Errorf("you cannot spawn sub-agents: sub-agents go at most %d levels deep", MaxSubagentDepth)
	}
	if maxSteps == 0 {
		maxSteps = DefaultSubagentMaxSteps
	}
	child, err := r.mgr.spawn(r, name, reason, task, maxSteps)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("started sub-agent %q (run %d): it works in the background; call wait_subagents to get its report", name, child.run.ID), nil
}

// Wait waits for the sub-agents and returns their reports.
func (t team) Wait(ctx context.Context, names []string) (string, error) {
	r := t.r
	var wanted []*member
	if len(names) == 0 {
		if wanted = r.kids.all(); len(wanted) == 0 {
			return "", errors.New("you have no sub-agents: spawn some with spawn_subagent first")
		}
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		m := r.kids.get(name)
		if m == nil {
			return "", fmt.Errorf("you have no sub-agent called %q (yours: %s)", name, r.kids.names(""))
		}
		wanted = append(wanted, m)
	}
	var sb strings.Builder
	for i, m := range wanted {
		select {
		case <-m.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "## %s (run %d): %s\n", m.name, m.id, m.out.status)
		if m.out.status == core.StatusCompleted {
			sb.WriteString(m.out.report)
		} else {
			fmt.Fprintf(&sb, "It did not finish: %s", m.out.err)
		}
	}
	return sb.String(), nil
}

// Send sends text to a sibling. Only the members of the family of the parent can be found, so a sub-agent of a sibling (a "cousin"),
// the parent and the agents of other researches cannot be written to.
func (t team) Send(ctx context.Context, to, text string) (string, error) {
	r := t.r
	if r.parent == nil {
		return "", errors.New("you have no siblings: only sub-agents can message each other")
	}
	sib := r.parent.kids.get(to)
	switch {
	case sib == nil:
		return "", fmt.Errorf("you have no sibling called %q (your siblings: %s)", to, orNone(r.parent.kids.names(r.run.Name)))
	case sib.id == r.run.ID:
		return "", errors.New("you cannot message yourself")
	}
	err := SendMessage(ctx, r.mgr.Messenger, Message{From: r.run.ID, FromName: r.run.Name, To: sib.id, Text: text})
	switch {
	case errors.Is(err, ErrNoInbox):
		return "", fmt.Errorf("sibling %q has ended: the message was not delivered", to)
	case errors.Is(err, ErrInboxFull):
		return "", fmt.Errorf("sibling %q has too many unread messages: try later", to)
	case err != nil:
		return "", err
	}
	return fmt.Sprintf("sent to %q", to), nil
}

// Siblings lists the other sub-agents of the parent of the agent, with their reasons and whether they still work: the agents it can
// message, and no others.
func (t team) Siblings(_ context.Context) (string, error) {
	r := t.r
	if r.parent == nil {
		return "", errors.New("you have no siblings: only sub-agents have them")
	}
	var sb strings.Builder
	n := 0
	for _, m := range r.parent.kids.all() {
		if m.id == r.run.ID {
			continue
		}
		status := "working"
		if m.ended() {
			status = "ended"
			if m.out.status == core.StatusFailed {
				status = "ended, failed"
			}
		}
		fmt.Fprintf(&sb, "- %s [%s]: %s\n", m.name, status, m.reason)
		n++
	}
	if n == 0 {
		return "You have no siblings now (your parent may still spawn some).", nil
	}
	return fmt.Sprintf("Your siblings, the sub-agents you can message (%d):\n%s", n, strings.TrimRight(sb.String(), "\n")), nil
}

// Receive returns the messages that were sent to the agent.
func (t team) Receive(ctx context.Context, wait time.Duration) (string, error) {
	msgs, err := t.r.mgr.Messenger.Receive(ctx, t.r.run.ID, wait)
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		return "No new messages.", nil
	}
	var sb strings.Builder
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "From %s (run %d): %s", m.FromName, m.From, m.Text)
	}
	return sb.String(), nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// spawn records a sub-agent of parent (a run with parent_id and name) and starts it in the background, on its own goroutine like
// every agent. It is called on the goroutine of parent, by its tool. Errors are shown to the model of the parent.
func (m *Manager) spawn(parent *researcher, name, reason, task string, maxSteps int) (*researcher, error) {
	switch {
	case !agent.ValidSubagentName(name):
		return nil, errors.New("name must be 1 to 40 letters, digits, - or _")
	case strings.TrimSpace(reason) == "":
		return nil, errors.New("reason is empty: say why this sub-agent has to exist")
	case strings.TrimSpace(task) == "":
		return nil, errors.New("task is empty")
	case maxSteps < 1 || maxSteps > MaxMaxSteps:
		return nil, fmt.Errorf("max_steps must be between 1 and %d", MaxMaxSteps)
	case m.ctx.Err() != nil:
		return nil, errors.New("the server is shutting down")
	}
	kids := parent.kids
	kids.mu.Lock() // the name check, the record and the entry in the family are one step
	defer kids.mu.Unlock()
	for _, k := range kids.members {
		if k.name == name {
			return nil, fmt.Errorf("you have a sub-agent called %q already: choose another name", name)
		}
	}
	if len(kids.members) >= MaxSubagentsPerAgent {
		return nil, fmt.Errorf("you have %d sub-agents already, the most one agent may have", MaxSubagentsPerAgent)
	}
	if !parent.tree.reserve() {
		return nil, fmt.Errorf("this research has %d sub-agents already, the most it may have", MaxSubagentsPerTree)
	}
	ok := false
	defer func() {
		if !ok {
			parent.tree.release()
		}
	}()

	parentID := parent.run.ID
	run := &core.Run{
		UserID: parent.run.UserID, ParentID: &parentID, Name: name, Reason: strings.TrimSpace(reason), Topic: strings.TrimSpace(task),
		Status: core.StatusRunning, State: core.StateStarting, Model: parent.opts.Model, MaxSteps: maxSteps,
	}
	dbCtx := context.WithoutCancel(m.ctx)
	err := m.tx(dbCtx, func(q db.Execer) error {
		if err := m.Ops.Store.CreateRun(dbCtx, q, run); err != nil {
			return err
		}
		_, err := m.Ops.Store.EnterState(dbCtx, q, run.ID, core.StateStarting, 0, "")
		return err
	})
	if err != nil {
		log.WithError(err).WithField("parent_id", parentID).Error("agent research: recording the sub-agent failed")
		return nil, errors.New("could not record the sub-agent")
	}
	opts := Options{Topic: run.Topic, MaxSteps: maxSteps, Model: run.Model, Dataset: parent.opts.Dataset}
	child := newResearcher(m, run, opts, parent.mem, parent)
	if err := m.Messenger.Open(run.ID); err != nil {
		log.WithError(err).WithField("run_id", run.ID).Error("agent research: opening the inbox failed")
	}
	kids.members = append(kids.members, child.self)
	ok = true

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		child.work()
	}()
	return child, nil
}
