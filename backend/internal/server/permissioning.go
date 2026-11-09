package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

var policyStaleError = apierr.New(api.ErrorCodeConflict, api.ErrorReasonPolicyStale, nil)

var policyAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

func normalizePolicyAddresses(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for i, v := range values {
		if !policyAddressPattern.MatchString(v) {
			return nil, fmt.Errorf("accountsAllowlist[%d]: must be a 0x address with 40 hex digits", i)
		}
		v = strings.ToLower(v)
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	sort.Strings(out)
	return out, nil
}
func (s *Server) GetPermissioningPolicy(ctx context.Context, _ api.GetPermissioningPolicyRequestObject) (api.GetPermissioningPolicyResponseObject, error) {
	policies, err := s.store.ListPermissioningPolicies(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	results := s.cachedPolicyStates(nodes, policies)
	// The frontend reads the toggle from here: list nodes with self-added addresses or with the
	// shared list turned off (the latter has revision 0 and an empty list when there is no row)
	custom := []api.NodeAllowlist{}
	for _, n := range nodes {
		if p, ok := policies[n.ID]; ok || !n.AllowlistShared {
			list := p.AccountsAllowlist
			if list == nil {
				list = []string{}
			}
			custom = append(custom, api.NodeAllowlist{NodeId: n.ID, Revision: p.Revision, AccountsAllowlist: list, UseShared: n.AllowlistShared})
		}
	}
	shared := policies[store.SharedPolicy]
	return api.GetPermissioningPolicy200JSONResponse{AccountsAllowlist: shared.AccountsAllowlist, Revision: shared.Revision, Nodes: &results, Custom: &custom}, nil
}

// allowlistMu: every allowlist write does read -> push -> save inside the lock, so saving the
// shared list and toggling a node at the same time cannot leave that node with a stale list.
// ponytail: one global lock (all writes are triggered by a person clicking); switch to one lock per
// node if different nodes need to write in parallel.
var allowlistMu sync.Mutex

// SetPermissioningPolicy saves the shared list, then pushes to the nodes that use the shared list,
// each getting its own effective list. The platform has no allowlist on/off toggle: whether a node
// has it enabled is decided by its startup flags, and the platform only detects it.
func (s *Server) SetPermissioningPolicy(ctx context.Context, req api.SetPermissioningPolicyRequestObject) (api.SetPermissioningPolicyResponseObject, error) {
	if req.Body == nil {
		return api.SetPermissioningPolicy400JSONResponse{Code: api.ErrorCodeInvalid, Message: "empty body"}, nil // the contract's required check rejects this first; unreachable
	}
	addresses, err := normalizePolicyAddresses(req.Body.AccountsAllowlist)
	if err != nil { // the contract's items pattern rejects this first; unreachable
		return api.SetPermissioningPolicy400JSONResponse{Code: api.ErrorCodeInvalid, Message: err.Error()}, nil
	}
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	policies, err := s.store.ListPermissioningPolicies(ctx)
	if err != nil {
		return nil, err
	}
	old := policies[store.SharedPolicy]
	if old.Revision != req.Body.Revision {
		return api.SetPermissioningPolicy409JSONResponse(policyStaleError), nil
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"accounts": len(addresses)}
	d["added"], d["removed"] = diffAddresses(old.AccountsAllowlist, addresses)
	if err := s.audit(ctx, "permissioning_policy_set", d); err != nil {
		return nil, err
	}
	revision, err := s.store.SavePermissioningPolicy(ctx, store.SharedPolicy, addresses, req.Body.Revision)
	if errors.Is(err, store.ErrPolicyStale) { // another platform process saved first
		s.auditFailedError(ctx, "permissioning_policy_set", d, policyStaleError)
		return api.SetPermissioningPolicy409JSONResponse(policyStaleError), nil
	}
	if err != nil {
		s.auditFailedInternal(ctx, "permissioning_policy_set", d, err)
		return nil, err
	}
	var targets []store.Node
	for _, n := range nodes {
		if n.AllowlistShared {
			targets = append(targets, n)
		}
	}
	results := s.pushAllowlist(ctx, targets, func(n store.Node) []string {
		return effectiveAllowlist(addresses, true, policies[n.ID].AccountsAllowlist)
	})
	// The version and per-node results are known only at the end; add a done entry
	s.auditDone(ctx, "permissioning_policy_set", map[string]any{"policy_revision": revision, "nodes": results})
	return api.SetPermissioningPolicy200JSONResponse{PolicyRevision: revision, Nodes: results}, nil
}

// nodeWrite is the result of a single-node write. conflict -> 409 (node not enabled, unreachable),
// failed -> 502 (node rejected it, read-back mismatch); neither is saved.
type nodeWrite struct {
	revision int64
	result   api.PolicyApplyNodeResult
	invalid  *api.Error
	notFound *api.Error
	conflict *api.Error
	failed   *api.Error
}

// writeNode: every single-node allowlist write goes through here (the caller holds allowlistMu):
// push to the node and read back first, persist only if they match.
func (s *Server) writeNode(ctx context.Context, n store.Node, want []string, event string, d map[string]any, persist func() (int64, error)) (nodeWrite, error) {
	if err := s.audit(ctx, event, d); err != nil {
		return nodeWrite{}, err
	}
	r := s.pushAllowlist(ctx, []store.Node{n}, func(store.Node) []string { return want })[0]
	out := nodeWrite{result: r}
	node := map[string]string{"nodeId": n.ID}
	switch r.State {
	case api.PolicyApplyInSync:
	case api.PolicyApplyDisabled:
		out.conflict = errPtr(apierr.New(api.ErrorCodeConflict, api.ErrorReasonAllowlistDisabled, node))
	case api.PolicyApplyUnreachable:
		out.conflict = errPtr(apierr.New(api.ErrorCodeConflict, api.ErrorReasonNodeUnreachable, node))
	case api.PolicyApplyRejected:
		out.failed = errPtr(itemError(r)) // the single-node page is this node itself: return its reason directly (the most fundamental one)
	default:
		out.failed = errPtr(apierr.New(api.ErrorCodeInternal, api.ErrorReasonAllowlistMismatch, node))
	}
	if e := cmp.Or(out.conflict, out.failed); e != nil {
		s.auditFailedError(ctx, event, d, *e)
		return out, nil
	}
	revision, err := persist()
	if errors.Is(err, store.ErrPolicyStale) { // another platform process saved first; the node already has the write, and the next save or re-apply overwrites it back
		s.auditFailedError(ctx, event, d, policyStaleError)
		return nodeWrite{conflict: errPtr(policyStaleError)}, nil
	}
	if err != nil {
		s.auditFailedInternal(ctx, event, d, err)
		return nodeWrite{}, err
	}
	out.revision = revision
	s.auditDone(ctx, event, map[string]any{"node_id": n.ID, "nodes": []api.PolicyApplyNodeResult{r}})
	return out, nil
}

// saveExtras saves this node's self-added addresses; if empty, it deletes the row and returns
// revision 0.
func (s *Server) saveExtras(ctx context.Context, nodeID string, extras []string, expected int64) (int64, error) {
	if len(extras) == 0 {
		_, err := s.store.DeletePermissioningPolicy(ctx, nodeID)
		return 0, err
	}
	return s.store.SavePermissioningPolicy(ctx, nodeID, extras, expected)
}

// ownNodePolicies is the shared start of single-node writes (the caller holds allowlistMu): confirm
// it is one of our nodes and read the current lists. A nil node = 404.
func (s *Server) ownNodePolicies(ctx context.Context, id string) (*store.Node, map[string]store.PermissioningPolicy, error) {
	n, err := s.requireOwnNode(ctx, id)
	if errors.Is(err, ErrNodeNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	policies, err := s.store.ListPermissioningPolicies(ctx)
	if err != nil {
		return nil, nil, err
	}
	return n, policies, nil
}

// SetNodeAllowlist sets this node's self-added addresses. When the shared list is applied,
// addresses already in the shared list are not stored separately.
func (s *Server) SetNodeAllowlist(ctx context.Context, req api.SetNodeAllowlistRequestObject) (api.SetNodeAllowlistResponseObject, error) {
	out, err := s.setNodeAllowlist(ctx, req.Id, req.Body)
	switch {
	case err != nil:
		return nil, err
	case out.notFound != nil:
		return api.SetNodeAllowlist404JSONResponse(*out.notFound), nil
	case out.invalid != nil:
		return api.SetNodeAllowlist400JSONResponse(*out.invalid), nil
	case out.conflict != nil:
		return api.SetNodeAllowlist409JSONResponse(*out.conflict), nil
	case out.failed != nil:
		return api.SetNodeAllowlist502JSONResponse(*out.failed), nil
	}
	return api.SetNodeAllowlist200JSONResponse{PolicyRevision: out.revision, Nodes: []api.PolicyApplyNodeResult{out.result}}, nil
}

func (s *Server) setNodeAllowlist(ctx context.Context, id string, body *api.PermissioningPolicy) (nodeWrite, error) {
	if body == nil {
		return nodeWrite{invalid: &api.Error{Code: api.ErrorCodeInvalid, Message: "empty body"}}, nil // the contract's required check rejects this first; unreachable
	}
	addresses, err := normalizePolicyAddresses(body.AccountsAllowlist)
	if err != nil { // the contract's items pattern rejects this first; unreachable
		return nodeWrite{invalid: &api.Error{Code: api.ErrorCodeInvalid, Message: err.Error()}}, nil
	}
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	n, policies, err := s.ownNodePolicies(ctx, id)
	if n == nil {
		return nodeWrite{notFound: errPtr(nodeNotFoundError)}, err
	}
	old, exists := policies[n.ID]
	if old.Revision != body.Revision { // old.Revision is 0 when there is no row
		return nodeWrite{conflict: errPtr(policyStaleError)}, nil
	}
	shared := policies[store.SharedPolicy].AccountsAllowlist
	extras := addresses
	if n.AllowlistShared {
		extras = without(addresses, shared)
	}
	d := map[string]any{"node_id": n.ID, "accounts": len(extras), "created": !exists}
	d["added"], d["removed"] = diffAddresses(old.AccountsAllowlist, extras)
	return s.writeNode(ctx, *n, effectiveAllowlist(shared, n.AllowlistShared, extras), "permissioning_node_set", d,
		func() (int64, error) { return s.saveExtras(ctx, n.ID, extras, body.Revision) })
}

// DeleteNodeAllowlist clears this node's self-added addresses (the toggle is unchanged).
func (s *Server) DeleteNodeAllowlist(ctx context.Context, req api.DeleteNodeAllowlistRequestObject) (api.DeleteNodeAllowlistResponseObject, error) {
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	n, policies, err := s.ownNodePolicies(ctx, req.Id)
	if n == nil {
		if err != nil {
			return nil, err
		}
		return api.DeleteNodeAllowlist404JSONResponse(nodeNotFoundError), nil
	}
	old, ok := policies[n.ID]
	if !ok {
		return api.DeleteNodeAllowlist404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonAllowlistNoExtras, nil)), nil
	}
	shared := policies[store.SharedPolicy]
	out, err := s.writeNode(ctx, *n, effectiveAllowlist(shared.AccountsAllowlist, n.AllowlistShared, nil), "permissioning_node_removed",
		map[string]any{"node_id": n.ID, "removed": old.AccountsAllowlist},
		func() (int64, error) { return s.saveExtras(ctx, n.ID, nil, 0) })
	switch {
	case err != nil:
		return nil, err
	case out.conflict != nil:
		return api.DeleteNodeAllowlist409JSONResponse(*out.conflict), nil
	case out.failed != nil:
		return api.DeleteNodeAllowlist502JSONResponse(*out.failed), nil
	}
	return api.DeleteNodeAllowlist200JSONResponse{PolicyRevision: shared.Revision, Nodes: []api.PolicyApplyNodeResult{out.result}}, nil
}

// SetNodeAllowlistShared sets whether this node applies the shared list. Turning it on removes
// self-added addresses that are already in the shared list, so addresses later deleted from the
// shared list disappear from this node too; an unchanged value counts as re-applying once.
func (s *Server) SetNodeAllowlistShared(ctx context.Context, req api.SetNodeAllowlistSharedRequestObject) (api.SetNodeAllowlistSharedResponseObject, error) {
	if req.Body == nil {
		return api.SetNodeAllowlistShared400JSONResponse{Code: api.ErrorCodeInvalid, Message: "empty body"}, nil // the contract's required check rejects this first; unreachable
	}
	on := req.Body.UseShared
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	n, policies, err := s.ownNodePolicies(ctx, req.Id)
	if n == nil {
		if err != nil {
			return nil, err
		}
		return api.SetNodeAllowlistShared404JSONResponse(nodeNotFoundError), nil
	}
	old := policies[n.ID]
	shared := policies[store.SharedPolicy].AccountsAllowlist
	extras := old.AccountsAllowlist
	if on {
		extras = without(extras, shared)
	}
	out, err := s.writeNode(ctx, *n, effectiveAllowlist(shared, on, extras), "permissioning_node_shared_set",
		map[string]any{"node_id": n.ID, "use_shared": on},
		func() (int64, error) { return s.store.SetNodeAllowlistShared(ctx, n.ID, on, extras, old.Revision) })
	switch {
	case err != nil:
		return nil, err
	case out.conflict != nil:
		return api.SetNodeAllowlistShared409JSONResponse(*out.conflict), nil
	case out.failed != nil:
		return api.SetNodeAllowlistShared502JSONResponse(*out.failed), nil
	}
	return api.SetNodeAllowlistShared200JSONResponse{PolicyRevision: out.revision, Nodes: []api.PolicyApplyNodeResult{out.result}}, nil
}

// SyncNodeAllowlist re-applies: pushes the platform's effective list to this node again (no data
// changes).
func (s *Server) SyncNodeAllowlist(ctx context.Context, req api.SyncNodeAllowlistRequestObject) (api.SyncNodeAllowlistResponseObject, error) {
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	n, policies, err := s.ownNodePolicies(ctx, req.Id)
	if n == nil {
		if err != nil {
			return nil, err
		}
		return api.SyncNodeAllowlist404JSONResponse(nodeNotFoundError), nil
	}
	shared := policies[store.SharedPolicy]
	out, err := s.writeNode(ctx, *n, effectiveAllowlist(shared.AccountsAllowlist, n.AllowlistShared, policies[n.ID].AccountsAllowlist),
		"permissioning_node_synced", map[string]any{"node_id": n.ID}, func() (int64, error) { return shared.Revision, nil })
	switch {
	case err != nil:
		return nil, err
	case out.conflict != nil:
		return api.SyncNodeAllowlist409JSONResponse(*out.conflict), nil
	case out.failed != nil:
		return api.SyncNodeAllowlist502JSONResponse(*out.failed), nil
	}
	return api.SyncNodeAllowlist200JSONResponse{PolicyRevision: out.revision, Nodes: []api.PolicyApplyNodeResult{out.result}}, nil
}

// RunInitialAllowlist pushes the effective list once to newly joined nodes
// (nodes.allowlist_initial_since non-NULL) after the platform can read their allowlist. It only
// covers this one join; later drift still needs someone to click re-apply.
func (s *Server) RunInitialAllowlist(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.pushInitialAllowlists(ctx); err != nil {
				log.Printf("permissioning: push allowlist to new node: %v", err)
			}
		}
	}
}

func (s *Server) pushInitialAllowlists(ctx context.Context) error {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.AllowlistInitialSince == nil {
			continue
		}
		if err := s.pushInitialAllowlist(ctx, n.ID); err != nil {
			return err
		}
	}
	return nil
}

// pushInitialAllowlist decides based on the allowlist the collector read after the join. If it
// cannot be read (including a disconnect at push time) it waits for the next round; it pushes only
// if the list on the node is empty and differs from the platform, and right before pushing it reads
// again live to confirm it is still empty (the cache can be up to one round stale, and addresses
// added on the node in that time must not be deleted by this push). If the node already has
// addresses it is left alone and shown as out of sync for someone to decide: the joining node may
// be an existing one, and an automatic push would replace its original list. Every other case
// clears the flag: a failed push shows up as the usual mismatch and is not retried every round.
func (s *Server) pushInitialAllowlist(ctx context.Context, id string) error {
	allowlistMu.Lock()
	defer allowlistMu.Unlock()
	n, policies, err := s.ownNodePolicies(ctx, id)
	if n == nil || err != nil || n.AllowlistInitialSince == nil {
		return err
	}
	want := effectiveAllowlist(policies[store.SharedPolicy].AccountsAllowlist, n.AllowlistShared, policies[n.ID].AccountsAllowlist)
	var snap *collector.Snapshot
	if res, ok := s.col.Get(n.ID); ok && res.At.After(*n.AllowlistInitialSince) {
		snap = res.Snap
	}
	r := cachedPolicyState(n.ID, snap, nil, want)
	if r.State == api.PolicyApplyUnreachable {
		return nil
	}
	if r.State == api.PolicyApplyDrift && len(*r.CurrentAllowlist) == 0 {
		live, err := s.ops.ReadAllowlist(ctx, n.ID)
		if err != nil {
			return nil
		}
		if len(live) > 0 {
			return s.store.ClearAllowlistInitial(ctx, n.ID)
		}
		noop := func() (int64, error) { return 0, nil }
		out, err := s.writeNode(ctx, *n, want, "permissioning_node_initial", map[string]any{"node_id": n.ID}, noop)
		if err != nil || out.result.State == api.PolicyApplyUnreachable {
			return err
		}
	}
	return s.store.ClearAllowlistInitial(ctx, n.ID)
}

// effectiveAllowlist: a node's effective list = (applies shared list ? shared list : empty) union
// self-added addresses. Pushing and comparing both use this.
func effectiveAllowlist(shared []string, useShared bool, extras []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(list []string) {
		for _, v := range list {
			if v = strings.ToLower(v); !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	if useShared {
		add(shared)
	}
	add(extras)
	sort.Strings(out)
	return out
}

// without returns list minus the entries in drop (case-insensitive).
func without(list, drop []string) []string {
	added, _ := diffAddresses(drop, list)
	return added
}

// pushAllowlist sends perm_* RPCs to every node in parallel (only the differences) and reads back
// to compare; want gives each node its own effective list.
func (s *Server) pushAllowlist(ctx context.Context, nodes []store.Node, want func(store.Node) []string) []api.PolicyApplyNodeResult {
	results := make([]api.PolicyApplyNodeResult, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n store.Node) {
			defer wg.Done()
			w := want(n)
			have, err := s.ops.SyncAllowlist(ctx, n.ID, w)
			if err == nil {
				s.pushedAllowlists.Store(n.ID, pushedAllowlist{list: have, at: time.Now().UTC()})
			}
			results[i] = syncedPolicyState(n.ID, have, err, w)
		}(i, n)
	}
	wg.Wait()
	return results
}

// syncedPolicyState is one node's result for PUT. SyncAllowlist reads the list first, so both
// reasons for not being enabled come back here.
func syncedPolicyState(nodeID string, have []string, err error, want []string) api.PolicyApplyNodeResult {
	r := api.PolicyApplyNodeResult{NodeId: nodeID}
	switch {
	case errors.Is(err, rpcops.ErrPermUnavailable), errors.Is(err, rpcops.ErrAllowlistDisabled):
		r.State = api.PolicyApplyDisabled
		setItemFailure(&r, err)
	case errors.Is(err, pki.ErrAgentOffline): // the status itself is "unreachable", no separate reason (must be checked before setItemFailure: this error also carries node_unreachable)
		r.State = api.PolicyApplyUnreachable
	case errors.As(err, new(*rpcops.RPCError)): // the request got through and Besu refused (e.g. could not write the permissions file), not unreachable
		r.State = api.PolicyApplyRejected
		setItemFailure(&r, err)
	case err != nil:
		r.State = api.PolicyApplyUnreachable
		setItemFailure(&r, err)
	default:
		compareAllowlist(&r, &have, want)
	}
	return r
}

// compareAllowlist, when the list is readable, includes the list and decides in_sync / drift
// (shared by GET and PUT).
func compareAllowlist(r *api.PolicyApplyNodeResult, have *[]string, want []string) {
	r.CurrentAllowlist = have
	r.State = api.PolicyApplyDrift
	if rpcops.SameAccounts(*have, want) {
		r.State = api.PolicyApplyInSync
	}
}

// pushedAllowlist is a node's allowlist as read back right after a successful push. Until the
// collector has a round that started after it, GET compares against this instead of the older
// round, so a save does not show drift for up to one collection round.
type pushedAllowlist struct {
	list []string
	at   time.Time
}

// cachedPolicyStates compares each node's allowlist from the collector's latest round with its
// effective list (without hitting the node live).
func (s *Server) cachedPolicyStates(nodes []store.Node, policies map[string]store.PermissioningPolicy) []api.PolicyApplyNodeResult {
	results := make([]api.PolicyApplyNodeResult, len(nodes))
	for i, n := range nodes {
		res, ok := s.col.Get(n.ID)
		var snap *collector.Snapshot
		if ok {
			snap = res.Snap
		}
		var pushed *pushedAllowlist
		if v, ok := s.pushedAllowlists.Load(n.ID); ok {
			p := v.(pushedAllowlist)
			pushed = &p
		}
		results[i] = cachedPolicyState(n.ID, snap, pushed, effectiveAllowlist(policies[store.SharedPolicy].AccountsAllowlist, n.AllowlistShared, policies[n.ID].AccountsAllowlist))
	}
	return results
}

// cachedPolicyState: not enabled = no allowlist file or no PERM namespace (in the latter case
// AllowlistEnabled is nil, so look at AllowlistStatus). pushed, when the round started before it,
// stands in for the round's list.
func cachedPolicyState(nodeID string, snap *collector.Snapshot, pushed *pushedAllowlist, want []string) api.PolicyApplyNodeResult {
	r := api.PolicyApplyNodeResult{NodeId: nodeID}
	switch {
	case snap == nil || snap.Ledger == nil:
		r.State = api.PolicyApplyUnreachable
	case pushed != nil && snap.AsOf.Before(pushed.at):
		list := slices.Clone(pushed.list)
		compareAllowlist(&r, &list, want)
	case snap.Allowlist != nil:
		compareAllowlist(&r, snap.Allowlist, want)
	case snap.AllowlistStatus.DisabledErr() != nil:
		r.State = api.PolicyApplyDisabled
		setItemFailure(&r, snap.AllowlistStatus.DisabledErr())
	default:
		r.State = api.PolicyApplyUnreachable
		setItemFailure(&r, errAllowlistRead)
	}
	return r
}

var errAllowlistRead = apierr.Wrap(api.ErrorReasonAllowlistReadFailed, nil, errors.New("cannot read node allowlist"))

// itemError converts one node's failure reason back into an error response (setItemFailure always
// sets a reason; unrecognized ones are operation_failed).
func itemError(r api.PolicyApplyNodeResult) api.Error {
	var p map[string]string
	if r.Params != nil {
		p = *r.Params
	}
	return apierr.New(api.ErrorCodeInternal, *r.Reason, p)
}

// setItemFailure sets one node's failure reason: use the reason if the underlying error carries
// one, put unrecognized raw text in detail; error is an English description.
func setItemFailure(r *api.PolicyApplyNodeResult, err error) {
	e := causeError(api.ErrorCodeInternal, api.ErrorReasonOperationFailed, err)
	r.Error, r.Reason, r.Params = &e.Message, e.Reason, e.Params
}

// diffAddresses returns the before/after allowlist difference (the audit records who changed, not
// just counts). Comparison is case-insensitive: the new list is normalized to lowercase, the stored
// old list is not guaranteed to be.
func diffAddresses(before, after []string) (added, removed []string) {
	had := make(map[string]bool, len(before))
	for _, a := range before {
		had[strings.ToLower(a)] = true
	}
	has := make(map[string]bool, len(after))
	added, removed = []string{}, []string{}
	for _, a := range after {
		has[strings.ToLower(a)] = true
		if !had[strings.ToLower(a)] {
			added = append(added, a)
		}
	}
	for _, a := range before {
		if !has[strings.ToLower(a)] {
			removed = append(removed, a)
		}
	}
	return added, removed
}
