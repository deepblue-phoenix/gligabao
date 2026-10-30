package agent_research

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Message is a text one agent sends to another.
type Message struct {
	// From and To are the IDs of the runs of the sender and the receiver; FromName is the name of the sender.
	From, To int64
	FromName string
	Text     string
	SentAt   time.Time
}

// Messenger carries the messages between agents: an agent has an inbox (Open), others put messages in it (Send) and it takes them
// out (Receive). It knows nothing about who may write to whom (that is decided by the code that calls Send: see team.go, where only
// siblings can) and nothing about what an agent is: only IDs. It is an interface so that the message system can be swapped (a
// queue, a database, another process) without touching the agents; the one that is used is chanMessenger, which is golang channels.
// The Manager has one (Manager.Messenger). Implementations must be safe for concurrent use.
type Messenger interface {
	// Open makes the inbox of agent id. Messages to an id that has no inbox fail.
	Open(id int64) error
	// Close removes the inbox of agent id, with the messages not read in it. Closing an unknown id does nothing.
	Close(id int64)
	// Send puts m in the inbox of m.To, without waiting for it to be read. ErrNoInbox if there is none (the agent ended), ErrInboxFull
	// if it holds too many messages.
	Send(ctx context.Context, m Message) error
	// Receive takes the messages out of the inbox of agent id, oldest first. If there are none and wait is positive, it waits up to
	// that long for one (and returns those that came with it). No messages is not an error. ErrNoInbox if the agent has no inbox.
	Receive(ctx context.Context, id int64, wait time.Duration) ([]Message, error)
}

// Errors of a Messenger.
var (
	ErrNoInbox   = errors.New("agent research: the agent has no inbox (it has ended)")
	ErrInboxFull = errors.New("agent research: the inbox of the agent is full")
)

// SendMessage is how the agents send a message: every send goes through this function, so that a different message system is a
// matter of a different Messenger (or of changing this function), and the agents are not touched. It sets the time of the message.
func SendMessage(ctx context.Context, mr Messenger, m Message) error {
	if m.SentAt.IsZero() {
		m.SentAt = time.Now().UTC()
	}
	return mr.Send(ctx, m)
}

// InboxSize is the number of messages the inbox of an agent holds, in the channel messenger.
const InboxSize = 64

// chanMessenger is the Messenger of Go channels: the inbox of an agent is a buffered channel of Messages.
type chanMessenger struct {
	mu      sync.RWMutex
	inboxes map[int64]chan Message
}

// NewChanMessenger returns a Messenger that carries the messages in Go channels (in memory, in this process).
func NewChanMessenger() Messenger {
	return &chanMessenger{inboxes: map[int64]chan Message{}}
}

func (c *chanMessenger) inbox(id int64) chan Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inboxes[id]
}

func (c *chanMessenger) Open(id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.inboxes[id]; ok {
		return fmt.Errorf("agent research: agent %d has an inbox already", id)
	}
	c.inboxes[id] = make(chan Message, InboxSize)
	return nil
}

// Close forgets the channel; it is not closed, so a sender that still holds it cannot panic.
func (c *chanMessenger) Close(id int64) {
	c.mu.Lock()
	delete(c.inboxes, id)
	c.mu.Unlock()
}

func (c *chanMessenger) Send(_ context.Context, m Message) error {
	ch := c.inbox(m.To)
	if ch == nil {
		return ErrNoInbox
	}
	select {
	case ch <- m:
		return nil
	default:
		return ErrInboxFull
	}
}

func (c *chanMessenger) Receive(ctx context.Context, id int64, wait time.Duration) ([]Message, error) {
	ch := c.inbox(id)
	if ch == nil {
		return nil, ErrNoInbox
	}
	var out []Message
	drain := func() {
		for {
			select {
			case m := <-ch:
				out = append(out, m)
			default:
				return
			}
		}
	}
	drain()
	if len(out) > 0 || wait <= 0 {
		return out, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case m := <-ch:
		out = append(out, m)
		drain()
	case <-timer.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return out, nil
}
