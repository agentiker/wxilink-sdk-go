package quota

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCounter_Lifecycle(t *testing.T) {
	c := New(Limit)
	if r := c.Remaining("u"); r != 10 {
		t.Fatalf("unseen user remaining = %d, want 10", r)
	}
	c.Sent("u", 3)
	if r := c.Remaining("u"); r != 7 {
		t.Errorf("after 3 sends = %d, want 7", r)
	}
	c.Sent("u", 9)
	if r := c.Remaining("u"); r != 0 {
		t.Errorf("overdrawn = %d, want 0", r)
	}
	// Measured: inbound resets to the limit, it does not add to the remainder.
	c.Inbound("u")
	if r := c.Remaining("u"); r != 10 {
		t.Errorf("after inbound = %d, want 10", r)
	}
	c.Sent("u", 2)
	c.Refused("u")
	if r := c.Remaining("u"); r != 0 {
		t.Errorf("after refused = %d, want 0", r)
	}
	if r := c.Remaining("other"); r != 10 {
		t.Errorf("users must be independent, other = %d", r)
	}
}

func TestCounter_Concurrent(t *testing.T) {
	c := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Sent("u", 1); c.Remaining("u") }()
	}
	wg.Wait()
	if r := c.Remaining("u"); r != 900 {
		t.Errorf("remaining = %d, want 900", r)
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q", "quota.json")
	c := New(Limit)
	c.Sent("a", 4)
	c.Inbound("b")
	c.Sent("b", 1)
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remaining("a") != 6 || got.Remaining("b") != 9 || got.Limit() != 10 {
		t.Errorf("round trip: a=%d b=%d limit=%d", got.Remaining("a"), got.Remaining("b"), got.Limit())
	}
	if got.Snapshot()["b"].Since.IsZero() {
		t.Error("Since not persisted")
	}

	fresh, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || fresh.Remaining("x") != 10 {
		t.Errorf("missing file: %v %d", err, fresh.Remaining("x"))
	}
}

func TestNudge(t *testing.T) {
	if n := Nudge(0); !strings.Contains(n, "回复任意消息") {
		t.Errorf("Nudge(0) = %q", n)
	}
	if n := Nudge(3); !strings.Contains(n, "3 条") {
		t.Errorf("Nudge(3) = %q", n)
	}
	if got := WithNudge("构建完成", 0); !strings.HasPrefix(got, "构建完成\n\n💬") {
		t.Errorf("WithNudge = %q", got)
	}
	if got := WithNudge("", 0); got != Nudge(0) {
		t.Errorf("WithNudge(empty) = %q", got)
	}
}

func TestCost(t *testing.T) {
	if Cost(false, false) != 1 || Cost(false, true) != 1 || Cost(true, false) != 1 || Cost(true, true) != 2 {
		t.Error("cost mismatch")
	}
}
