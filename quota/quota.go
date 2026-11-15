// Package quota models iLink's per-recipient send allowance.
//
// Measured against the live server (see README): each message a user sends
// to the bot allows the bot to send that user Limit (10) messages. The
// allowance resets to Limit on every inbound message — it does not
// accumulate. Once used up, sendmessage fails with ret=-2 until the user
// writes again.
//
// Users are not told about this. The convention used here is to make the
// last allowed message carry a short nudge asking them to reply, so that
// nothing is silently lost.
//
// Every sendmessage call costs 1: a media file sent with a caption costs 2
// (the caption is a separate text message).
package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Limit is the number of messages the bot may send per inbound user message.
const Limit = 10

// Nudge returns the reminder to put in the last message before the
// allowance runs out. pending is the number of messages still waiting to be
// delivered after this one (0 if none).
func Nudge(pending int) string {
	if pending > 0 {
		return fmt.Sprintf("💬 还有 %d 条消息待接收，回复任意消息即可查看", pending)
	}
	return "💬 回复任意消息，可继续接收后续通知"
}

// WithNudge appends Nudge(pending) to text.
func WithNudge(text string, pending int) string {
	if text == "" {
		return Nudge(pending)
	}
	return text + "\n\n" + Nudge(pending)
}

// Cost returns how many sendmessage calls a message consumes.
func Cost(hasMedia, hasCaption bool) int {
	if hasMedia && hasCaption {
		return 2
	}
	return 1
}

// State is the per-recipient counter.
type State struct {
	Used int `json:"used"`
	// Since is when the allowance was last reset (inbound message seen, or
	// inferred). Zero if never.
	Since time.Time `json:"since,omitempty"`
}

// Counter tracks sends per recipient since their last inbound message.
// It is safe for concurrent use and serialises to JSON.
//
// A Counter only knows what it is told: sends made by other processes with
// the same bot are invisible, so treat a server ret=-2 as authoritative
// ([Counter.Refused]).
type Counter struct {
	mu    sync.Mutex
	limit int
	users map[string]*State
}

// New returns a Counter with the given limit (use [Limit]).
func New(limit int) *Counter {
	if limit <= 0 {
		limit = Limit
	}
	return &Counter{limit: limit, users: map[string]*State{}}
}

func (c *Counter) state(user string) *State {
	s := c.users[user]
	if s == nil {
		s = &State{}
		c.users[user] = s
	}
	return s
}

// Limit returns the counter's per-inbound limit.
func (c *Counter) Limit() int { return c.limit }

// Remaining returns how many more messages may be sent to user. A user the
// counter has never seen is assumed to have a full allowance.
func (c *Counter) Remaining(user string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.users[user]
	if s == nil {
		return c.limit
	}
	if r := c.limit - s.Used; r > 0 {
		return r
	}
	return 0
}

// Inbound records that user sent the bot a message: the allowance resets.
func (c *Counter) Inbound(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state(user)
	s.Used = 0
	s.Since = time.Now()
}

// Sent records n successful sends to user.
func (c *Counter) Sent(user string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state(user).Used += n
}

// Refused records that the server refused a send (ret=-2): the allowance is
// used up regardless of what the counter believed.
func (c *Counter) Refused(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state(user).Used = c.limit
}

// Snapshot returns a copy of all per-recipient states.
func (c *Counter) Snapshot() map[string]State {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]State, len(c.users))
	for k, v := range c.users {
		out[k] = *v
	}
	return out
}

type fileFormat struct {
	Limit int               `json:"limit"`
	Users map[string]*State `json:"users"`
}

// MarshalJSON implements json.Marshaler.
func (c *Counter) MarshalJSON() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.Marshal(fileFormat{Limit: c.limit, Users: c.users})
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Counter) UnmarshalJSON(data []byte) error {
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = f.Limit
	if c.limit <= 0 {
		c.limit = Limit
	}
	c.users = f.Users
	if c.users == nil {
		c.users = map[string]*State{}
	}
	return nil
}

// Load reads a counter saved with [Counter.Save]. A missing file yields a
// fresh counter.
func Load(path string) (*Counter, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(Limit), nil
	}
	if err != nil {
		return nil, err
	}
	c := New(Limit)
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("quota: parse %s: %w", path, err)
	}
	return c, nil
}

// Save writes the counter to path atomically (0600).
func (c *Counter) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
