package collector

import (
	"testing"
	"time"
)

// The FailSince state machine (see GetFleetStatus):
// never failed -> just failed -> still failing (timer not reset) -> recovered (cleared).
func TestNextResultTracksFailSince(t *testing.T) {
	t0 := time.Now().UTC()
	t1 := t0.Add(5 * time.Second)
	t2 := t0.Add(10 * time.Second)
	t3 := t0.Add(15 * time.Second)

	// Never polled, succeeds the first time: EverSeen=true, FailSince=nil.
	r1 := nextResult(Result{}, &Snapshot{}, t0)
	if !r1.EverSeen || r1.FailSince != nil {
		t.Fatalf("首次成功應 EverSeen=true FailSince=nil,got %+v", r1)
	}

	// Succeeded before, fails this round: FailSince is set to this round's time.
	r2 := nextResult(r1, nil, t1)
	if r2.FailSince == nil || !r2.FailSince.Equal(t1) {
		t.Fatalf("剛失敗應把 FailSince 設成本輪時間,got %+v", r2)
	}
	if !r2.EverSeen {
		t.Error("EverSeen 應維持 true(不因這輪失敗而重置)")
	}

	// Still failing: FailSince keeps the time of the first failure and is not pushed forward by this round.
	r3 := nextResult(r2, nil, t2)
	if r3.FailSince == nil || !r3.FailSince.Equal(t1) {
		t.Fatalf("連續失敗不該重置計時,want %v got %+v", t1, r3)
	}

	// Recovered: FailSince is cleared.
	r4 := nextResult(r3, &Snapshot{}, t3)
	if r4.FailSince != nil {
		t.Fatalf("恢復後 FailSince 應清空,got %+v", r4)
	}
}

// Never succeeded (just joined, still warming up) and keeps failing: EverSeen stays false, but FailSince still starts timing
// (GetFleetStatus uses EverSeen to additionally exclude this "never connected" case, not by leaving FailSince unset here).
func TestNextResultNeverSeenStillTracksFailSince(t *testing.T) {
	t0 := time.Now().UTC()
	r := nextResult(Result{}, nil, t0)
	if r.EverSeen {
		t.Error("從未成功過,EverSeen 應為 false")
	}
	if r.FailSince == nil || !r.FailSince.Equal(t0) {
		t.Errorf("即使從未成功,FailSince 仍該記錄這輪失敗時間,got %+v", r)
	}
}
