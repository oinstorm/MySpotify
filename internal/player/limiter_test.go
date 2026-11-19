package player

import (
	"testing"
	"time"
)

// A refusal shuts the gate for as long as Spotify asked, and no licence is
// asked before it opens: asking again at once is what keeps a run of
// refusals going.
func TestGateWaitsAsAsked(t *testing.T) {
	var g licenceGate
	now := time.Now()
	until := g.refused(now, 7*time.Second)
	if got := until.Sub(now); got != 7*time.Second {
		t.Fatalf("waits %v, want 7s", got)
	}
	if !g.shut(now.Add(6 * time.Second)) {
		t.Fatal("open before the wait is over")
	}
	if g.shut(now.Add(8 * time.Second)) {
		t.Fatal("still shut after the wait")
	}
}

// Without a wait from Spotify, each refusal in a row waits longer: the first
// 30 seconds, then 60, then 120, up to the longest it goes.
func TestGateBacksOffFurther(t *testing.T) {
	var g licenceGate
	now := time.Now()
	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 8 * time.Minute, 8 * time.Minute, 8 * time.Minute,
	}
	for i, w := range want {
		until := g.refused(now, 0)
		if got := until.Sub(now); got != w {
			t.Fatalf("refusal %d waits %v, want %v", i+1, got, w)
		}
		now = until
	}
}

// A quiet spell without a refusal puts the wait back to its first step:
// being limited once an hour is not one long run.
func TestGateForgetsAfterAQuietSpell(t *testing.T) {
	var g licenceGate
	now := time.Now()
	g.refused(now, 0)
	now = now.Add(gateQuiet)
	until := g.refused(now, 0)
	if got := until.Sub(now); got != 30*time.Second {
		t.Fatalf("after a quiet spell waits %v, want 30s", got)
	}
}

// A track that plays long enough after a refusal puts the wait back to its
// first step: a run of refusals is over once a licence has played for half a
// minute.
func TestGateLetsGoAfterPlaying(t *testing.T) {
	var g licenceGate
	now := time.Now()
	g.refused(now, 0)
	if g.fails != 1 {
		t.Fatalf("fails %d after one refusal, want 1", g.fails)
	}
	g.started(now)
	g.playedFor(now.Add(gateGood - time.Second))
	if g.fails != 1 {
		t.Fatalf("forgot the run before it played long enough: fails %d", g.fails)
	}
	g.playedFor(now.Add(gateGood))
	if g.fails != 0 {
		t.Fatalf("fails %d after playing long enough, want 0", g.fails)
	}
	until := g.refused(now.Add(gateGood), 0)
	if got := until.Sub(now.Add(gateGood)); got != 30*time.Second {
		t.Fatalf("after playing waits %v, want 30s", got)
	}
}
