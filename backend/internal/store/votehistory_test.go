package store

import (
	"context"
	"testing"
	"time"
)

// Vote records: only one in-progress row per address and direction; a new row can be opened after it ends;
// the same vote (address, direction, end block) is not duplicated; ended ones come newest first.
func TestVoteRecords(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000aa"
	at := time.Unix(1_700_000_000, 0).UTC()
	b := int64(10)
	open := VoteRecord{Address: addr, Add: true, Result: "open", FirstBlock: 10, FirstAt: at, Votes: 1, Needed: 3,
		Ballots: []Ballot{{Address: "0x01", Block: &b, At: &at}, {Address: "0x02"}}}
	if err := s.SaveVoteRecords(ctx, []VoteRecord{open, open}); err != nil {
		t.Fatal(err)
	}
	opens, err := s.ListOpenVoteRecords(ctx)
	if err != nil || len(opens) != 1 || len(opens[0].Ballots) != 2 || opens[0].Ballots[1].Block != nil {
		t.Fatalf("只該有一列進行中:%+v %v", opens, err)
	}
	// Close as passed, and open a new in-progress row in the same round
	r := opens[0]
	end, endAt := int64(21), at.Add(22*time.Second)
	r.Result, r.EndedBlock, r.EndedAt = "passed", &end, &endAt
	if err := s.SaveVoteRecords(ctx, []VoteRecord{r, open}); err != nil {
		t.Fatal(err)
	}
	// Backfilling the same vote: unique-key collision, skipped
	dup := r
	dup.ID = 0
	if err := s.SaveVoteRecords(ctx, []VoteRecord{dup}); err != nil {
		t.Fatal(err)
	}
	end2, endAt2 := int64(40), at.Add(60*time.Second)
	later := VoteRecord{Address: addr, Add: false, Result: "expired", FirstBlock: 30, FirstAt: at, EndedBlock: &end2, EndedAt: &endAt2, Ballots: []Ballot{}}
	if err := s.SaveVoteRecords(ctx, []VoteRecord{later}); err != nil {
		t.Fatal(err)
	}
	closed, err := s.ListEndedVoteRecords(ctx)
	if err != nil || len(closed) != 2 || *closed[0].EndedBlock != 40 || closed[1].Result != "passed" {
		t.Fatalf("已結束:%+v %v", closed, err)
	}
	if opens, _ := s.ListOpenVoteRecords(ctx); len(opens) != 1 || opens[0].ID == r.ID {
		t.Fatalf("新開的進行中:%+v", opens)
	}
}

// The vote being closed already has another row recording it: do not overwrite; delete this in-progress row,
// and the whole batch still writes.
func TestVoteRecordCloseConflict(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000cc"
	at := time.Unix(1_700_000_000, 0).UTC()
	end := int64(21)
	if err := s.SaveVoteRecords(ctx, []VoteRecord{
		{Address: addr, Add: true, Result: VotePassed, FirstBlock: 10, FirstAt: at, EndedBlock: &end, EndedAt: &at, Ballots: []Ballot{}},
		{Address: addr, Add: true, Result: VoteOpen, FirstBlock: 10, FirstAt: at, Ballots: []Ballot{}},
	}); err != nil {
		t.Fatal(err)
	}
	opens, _ := s.ListOpenVoteRecords(ctx)
	r := opens[0]
	r.Result, r.EndedBlock, r.EndedAt = VotePassed, &end, &at
	other := VoteRecord{Address: addr, Add: false, Result: VoteOpen, FirstBlock: 30, FirstAt: at, Ballots: []Ballot{}}
	if err := s.SaveVoteRecords(ctx, []VoteRecord{r, other}); err != nil {
		t.Fatalf("撞唯一鍵不該讓整批失敗:%v", err)
	}
	ended, _ := s.ListEndedVoteRecords(ctx)
	opens, _ = s.ListOpenVoteRecords(ctx)
	if len(ended) != 1 || len(opens) != 1 || opens[0].Add {
		t.Fatalf("ended %+v open %+v", ended, opens)
	}
}
