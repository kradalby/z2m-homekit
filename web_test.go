package z2mhomekit

import (
	"fmt"
	"sync"
	"testing"
)

// eventLog is appended to from concurrent HTTP handlers and read by the index
// renderer. It was the one shared field on WebServer with no mutex; run this
// under -race to keep it that way.
func TestEventLogConcurrentAccess(t *testing.T) {
	ws := &WebServer{}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				ws.LogEvent(fmt.Sprintf("writer %d event %d", i, j))
			}
		})
		wg.Go(func() {
			for range 50 {
				for _, entry := range ws.recentEvents(20) {
					_ = entry
				}
			}
		})
	}
	wg.Wait()

	if got := len(ws.recentEvents(20)); got != 20 {
		t.Errorf("recentEvents(20) returned %d entries, want 20", got)
	}
}

func TestRecentEventsNewestFirstAndBounded(t *testing.T) {
	ws := &WebServer{}
	for i := range 5 {
		ws.LogEvent(fmt.Sprintf("e%d", i))
	}

	got := ws.recentEvents(3)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}

	// Entries are timestamped, so match on the suffix we control.
	for i, want := range []string{"e4", "e3", "e2"} {
		if len(got[i]) < len(want) || got[i][len(got[i])-len(want):] != want {
			t.Errorf("entry %d = %q, want it to end in %q", i, got[i], want)
		}
	}

	if n := len(ws.recentEvents(100)); n != 5 {
		t.Errorf("recentEvents(100) = %d entries, want 5 (only 5 logged)", n)
	}
}
