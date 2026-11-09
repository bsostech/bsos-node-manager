package rpcops

import (
	"context"
	"fmt"
)

// voteNS: the IBFT2 / QBFT RPC namespace. An empty consensus string counts as IBFT2.
func voteNS(consensus string) string {
	if consensus == "qbft" {
		return "qbft"
	}
	return "ibft"
}

// ProposeVote: casts a validator add/remove vote on a node (ibft_ / qbft_proposeValidatorVote), called
// directly by the platform without agent interpretation. The vote stays in the node's in-memory pending votes
// and is included each time the node proposes a block, until discarded or Besu restarts; the collector reads
// vote status from *_getPendingVotes.
func (c *Client) ProposeVote(ctx context.Context, nodeID, consensus, address string, add bool) error {
	ns := voteNS(consensus)
	var ok bool
	if err := c.Call(ctx, nodeID, ns+"_proposeValidatorVote", []any{address, add}, &ok); err != nil {
		return err
	}
	if !ok { // Besu 26.7's Ibft / QbftProposeValidatorVote always returns true (unsupported means a JSON-RPC error); this is just a safeguard, so no reason is given
		return fmt.Errorf("%s_proposeValidatorVote: node returned false", ns)
	}
	return nil
}

// PendingVotes: the node's local pending vote list (*_getPendingVotes), address -> true add / false remove.
func (c *Client) PendingVotes(ctx context.Context, nodeID, consensus string) (map[string]bool, error) {
	var out map[string]bool
	err := c.Call(ctx, nodeID, voteNS(consensus)+"_getPendingVotes", []any{}, &out)
	return out, err
}

// DiscardVote: discards the node's pending vote for this address (*_discardValidatorVote). The returned bool
// is not checked: Besu 26.7's IbftDiscardValidatorVote / QbftDiscardValidatorVote is hard-coded to return
// true (even when the node has no such vote); failures only come back as JSON-RPC errors (e.g.
// METHOD_NOT_ENABLED in QBFT contract mode), and on that path Call already returns an error.
func (c *Client) DiscardVote(ctx context.Context, nodeID, consensus, address string) error {
	ns := voteNS(consensus)
	var ok bool
	return c.Call(ctx, nodeID, ns+"_discardValidatorVote", []any{address}, &ok)
}
