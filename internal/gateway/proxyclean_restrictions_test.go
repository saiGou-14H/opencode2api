package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestProxyCleanRestrictionsBlockEntireLane(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		p, now := fixtureProxyClean(t)
		p.refresh(context.Background())
		lane := &anonymousNodes{dynamic: p, cooldown: time.Minute}
		cursor := lane.CursorFor("")
		node := cursor.Next()
		lane.MarkFailure(node, &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"600"}}}, nil)
		if p.current() != nil || cursor.Next() != nil {
			t.Fatalf("status %d allowed switching IP", status)
		}
		if p.blockedUntil.Load() < time.Now().Add(590*time.Second).UnixNano() {
			t.Fatal("Retry-After was shortened")
		}
		if p.view().LastError != "upstream_restricted" || p.view().CooldownUntil == nil {
			t.Fatal("restriction not visible")
		}
		until := p.blockedUntil.Load()
		// The node's legacy 8x cooldown expires before the lane's full 600s
		// Retry-After; a passing probe may recover the node, but not the lane.
		*now = now.Add(481 * time.Second)
		p.refresh(context.Background())
		p.check(context.Background())
		if node.failures.Load() != 0 {
			t.Fatal("fixture did not exercise successful recovery probe")
		}
		if p.blockedUntil.Load() != until || p.current() != nil {
			t.Fatal("source refresh or homepage probe cleared business cooldown")
		}
	}
}
