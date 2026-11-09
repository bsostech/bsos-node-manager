package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/store"
)

// TTL options (contract JoinTokenTtl, default 1h)
var joinTokenTTLs = map[api.JoinTokenTtl]time.Duration{
	api.JoinTokenTtlN15m: 15 * time.Minute,
	api.JoinTokenTtlN1h:  time.Hour,
	api.JoinTokenTtlN24h: 24 * time.Hour,
}

// ── POST /join-tokens ───────────────────────────────────────

// nodeIDPattern is the same rule as the contract's JoinTokenCreateRequest.nodeId; the id goes into
// the certificate CN and Prometheus labels.
var nodeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// maxAllowedSources caps the source IP allowlist. A node usually egresses from one or two
// addresses; the cap only guards against pasting a whole list by mistake.
const maxAllowedSources = 32

// normSources normalizes the source IP allowlist into CIDR strings (a single IP gets /32 or /128),
// deduplicated, keeping input order. Non-nil return = bad format or over the cap. Empty list = no
// restriction.
func normSources(in *[]string) ([]string, *api.Error) {
	out := []string{}
	if in == nil {
		return out, nil
	}
	seen := map[string]bool{}
	for _, raw := range *in {
		nets, err := reqinfo.ParseCIDRs(raw)
		if err != nil || len(nets) > 1 {
			return nil, invalidReason(api.ErrorReasonAllowedSourceInvalid, map[string]string{"value": strconv.Quote(raw)})
		}
		if len(nets) == 0 {
			continue // blank entry
		}
		if c := nets[0].String(); !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) > maxAllowedSources {
		return nil, invalidReason(api.ErrorReasonAllowedSourcesTooMany, map[string]string{"limit": strconv.Itoa(maxAllowedSources)})
	}
	return out, nil
}

// normNodeID always lowercases before checking the format. The id goes into the certificate CN,
// URLs and Prometheus labels; mixed case would turn into two nodes.
func normNodeID(raw string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(raw))
	return id, nodeIDPattern.MatchString(id)
}

func (s *Server) CreateJoinToken(ctx context.Context, req api.CreateJoinTokenRequestObject) (api.CreateJoinTokenResponseObject, error) {
	if req.Body == nil {
		return api.CreateJoinToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing nodeId"}, nil // the contract's required check rejects this first; unreachable
	}
	nodeID, ok := normNodeID(req.Body.NodeId)
	if !ok {
		return api.CreateJoinToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: "nodeId must be letters, digits and hyphens (1-63 characters, not starting with a hyphen)"}, nil // the contract's pattern rejects this first; unreachable
	}
	nodeName, msg := normNodeName(req.Body.NodeName)
	if msg != "" { // the contract's maxLength rejects this first; unreachable
		return api.CreateJoinToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: msg}, nil
	}
	rpcPort, metricsPort := 8545, 9545 // matches the contract default
	if req.Body.RpcPort != nil {
		rpcPort = *req.Body.RpcPort
	}
	if req.Body.MetricsPort != nil {
		metricsPort = *req.Body.MetricsPort
	}
	if !validPort(rpcPort) || !validPort(metricsPort) {
		return api.CreateJoinToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: "rpcPort / metricsPort must be 1-65535"}, nil // the contract's minimum / maximum rejects this first; unreachable
	}
	sources, e := normSources(req.Body.AllowedSources)
	if e != nil {
		return api.CreateJoinToken400JSONResponse(*e), nil
	}
	ttl := time.Hour
	if req.Body.Ttl != nil {
		if d, ok := joinTokenTTLs[*req.Body.Ttl]; ok {
			ttl = d
		}
	}

	secret := make([]byte, 24)
	rand.Read(secret)
	// token = BSOS-1-sha256:<root CA digest>-<secret>; the digest lets the agent verify the
	// platform against MITM
	token := fmt.Sprintf("BSOS-1-sha256:%s-%s", s.rootDigest, base64.RawURLEncoding.EncodeToString(secret))
	sum := sha256.Sum256([]byte(token))

	idb := make([]byte, 4)
	rand.Read(idb)
	now := time.Now().UTC()
	rec := store.JoinToken{
		ID:             "jt-" + hex.EncodeToString(idb),
		NodeName:       nodeName,
		AllowedSources: sources,
		SecretHash:     hex.EncodeToString(sum[:]),
		Status:         "pending",
		NodeID:         nodeID,
		RpcPort:        rpcPort,
		MetricsPort:    metricsPort,
		CreatedAt:      now,
		ExpiresAt:      now.Add(ttl),
	}
	// The token itself is not recorded; token_hash is the same value as in the join / re_enroll /
	// revoke records, so the three can be linked
	d := map[string]any{"code_id": rec.ID, "token_hash": rec.SecretHash, "node_id": nodeID, "node_name": nodeName, "expires_at": rec.ExpiresAt, "allowed_sources": sources}
	// The node's older unused codes are revoked in the same transaction: a node only ever consumes one
	// code, and an old one left behind (regenerated after leaving the wizard, rejoin pressed twice)
	// would stay valid, unseen, until it expires
	superseded := withActor(ctx, map[string]any{"reason": "superseded", "superseded_by": rec.ID})
	switch err := s.store.CreateJoinTokenSuperseding(ctx, rec, withActor(ctx, d), superseded); {
	case errors.Is(err, errAudit):
		return nil, err
	case err != nil:
		s.auditFailedInternal(ctx, "join_token_created", d, err)
		return nil, err
	}

	return api.CreateJoinToken201JSONResponse{
		Id:             rec.ID,
		NodeName:       rec.NodeName,
		AllowedSources: rec.AllowedSources,
		NodeId:         rec.NodeID,
		Status:         api.JoinTokenStatusPending,
		CreatedAt:      rec.CreatedAt,
		ExpiresAt:      rec.ExpiresAt,
		Token:          token, // appears only in this response; the DB has only the hash
		PlatformAddr:   s.platformAddr,
	}, nil
}

// ── POST /join-tokens/{id}/revoke ───────────────────────────

func (s *Server) RevokeJoinToken(ctx context.Context, req api.RevokeJoinTokenRequestObject) (api.RevokeJoinTokenResponseObject, error) {
	t, err := s.store.GetJoinToken(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return api.RevokeJoinToken404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonJoinTokenNotFound, nil)), nil
	}
	now := time.Now().UTC()
	d := map[string]any{"code_id": t.ID, "token_hash": t.SecretHash, "node_id": t.NodeID}
	// Expired tokens can be revoked too: "移除" (remove) on a not-yet-joined node in the node list
	// revokes its expired code. A used or already revoked one is refused and recorded, so an attempt
	// to revoke a code a node has already joined with shows up in the audit log.
	if st := t.EffectiveStatus(now); st != "pending" && st != "expired" {
		e := apierr.New(api.ErrorCodeConflict, api.ErrorReasonJoinTokenNotRevocable, nil)
		s.auditFailedError(ctx, "join_token_revoked", d, e)
		return api.RevokeJoinToken409JSONResponse(e), nil
	}
	if err := s.audit(ctx, "join_token_revoked", d); err != nil {
		return nil, err
	}
	ok, err := s.store.MarkJoinTokenRevoked(ctx, t.ID)
	if err != nil {
		s.auditFailedInternal(ctx, "join_token_revoked", d, err)
		return nil, err
	}
	if !ok { // a join used it (or another revoke got there) after the read above
		e := apierr.New(api.ErrorCodeConflict, api.ErrorReasonJoinTokenNotRevocable, nil)
		s.auditFailedError(ctx, "join_token_revoked", d, e)
		return api.RevokeJoinToken409JSONResponse(e), nil
	}
	t.Status = "revoked"
	return api.RevokeJoinToken200JSONResponse(toAPIJoinToken(*t, now)), nil
}

// ── GET /join-tokens ────────────────────────────────────────

func (s *Server) ListJoinTokens(ctx context.Context, _ api.ListJoinTokensRequestObject) (api.ListJoinTokensResponseObject, error) {
	tokens, err := s.store.ListJoinTokens(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resp := make(api.ListJoinTokens200JSONResponse, 0, len(tokens))
	for _, t := range tokens {
		resp = append(resp, toAPIJoinToken(t, now))
	}
	return resp, nil
}

func toAPIJoinToken(t store.JoinToken, now time.Time) api.JoinToken {
	return api.JoinToken{
		Id:             t.ID,
		NodeName:       t.NodeName,
		AllowedSources: t.AllowedSources,
		Status:         api.JoinTokenStatus(t.EffectiveStatus(now)),
		NodeId:         t.NodeID,
		CreatedAt:      t.CreatedAt.UTC(),
		ExpiresAt:      t.ExpiresAt.UTC(),
	}
}
