package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Vote record results (validator_vote_history.result in schema.sql).
const (
	VoteOpen    = "open"
	VotePassed  = "passed"
	VoteExpired = "expired"
)

// VoteRecord: one validator vote (see validator_vote_history in schema.sql). Result is one of the three above.
type VoteRecord struct {
	ID         int64
	Address    string
	Add        bool
	Result     string
	FirstBlock int64
	FirstAt    time.Time
	EndedBlock *int64
	EndedAt    *time.Time
	Votes      int
	Needed     int
	Ballots    []Ballot
}

// Ballot: one validator's vote in this round; Block / At are nil if they did not vote.
// WithdrawnAt: this org withdrew the vote (when the canceling vote was written into a block); Block / At
// still refer to the original vote, which no longer counts.
type Ballot struct {
	Address     string     `json:"address"`
	Block       *int64     `json:"block"`
	At          *time.Time `json:"at"`
	WithdrawnAt *time.Time `json:"withdrawnAt,omitempty"`
}

const voteRecordCols = `id, address, add_validator, result, first_block, first_at, ended_block, ended_at, votes, needed, ballots`

// ListOpenVoteRecords: in-progress ones (for the recorder).
func (s *Store) ListOpenVoteRecords(ctx context.Context) ([]VoteRecord, error) {
	return s.listVoteRecords(ctx, `WHERE ended_block IS NULL ORDER BY id`)
}

// ListEndedVoteRecords: ended ones, newest first (for the API).
func (s *Store) ListEndedVoteRecords(ctx context.Context) ([]VoteRecord, error) {
	return s.listVoteRecords(ctx, `WHERE ended_block IS NOT NULL ORDER BY ended_at DESC, id DESC`)
}

func (s *Store) listVoteRecords(ctx context.Context, where string) ([]VoteRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+voteRecordCols+` FROM validator_vote_history `+where)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[VoteRecord])
}

// SaveVoteRecords: one round's changes go in a single transaction. ID 0 = insert (skipped on a unique-key
// collision: the same vote is already recorded); otherwise the whole row is overwritten by ID.
func (s *Store) SaveVoteRecords(ctx context.Context, recs []VoteRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range recs {
		if r.ID == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO validator_vote_history (address, add_validator, result, first_block, first_at, ended_block, ended_at, votes, needed, ballots)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
				r.Address, r.Add, r.Result, r.FirstBlock, r.FirstAt, r.EndedBlock, r.EndedAt, r.Votes, r.Needed, r.Ballots)
		} else {
			// The vote being closed already has another row recording it (e.g. backfilled during downtime):
			// do not overwrite; delete this in-progress row instead, so the unique-key collision does not
			// fail the whole round
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `UPDATE validator_vote_history SET result = $2, first_block = $3, first_at = $4, ended_block = $5, ended_at = $6,
				votes = $7, needed = $8, ballots = $9 WHERE id = $1 AND NOT EXISTS (
					SELECT 1 FROM validator_vote_history o WHERE o.id <> $1 AND o.address = $10 AND o.add_validator = $11 AND o.ended_block = $5)`,
				r.ID, r.Result, r.FirstBlock, r.FirstAt, r.EndedBlock, r.EndedAt, r.Votes, r.Needed, r.Ballots, r.Address, r.Add)
			if err == nil && tag.RowsAffected() == 0 && r.EndedBlock != nil {
				_, err = tx.Exec(ctx, `DELETE FROM validator_vote_history WHERE id = $1 AND ended_block IS NULL`, r.ID)
			}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
