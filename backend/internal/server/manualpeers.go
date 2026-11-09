package server

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Shared list (manual_peers): nodes the user explicitly wants to connect to, one list shared across
// our organization and pushed to nodes whose mode is the shared list. The platform does not judge
// who the other side is or whether it should be connected: where a node's connections come from
// varies by deployment (static-nodes, discovery, inbound from the other side), and the platform
// assumes nothing; when a direct entry is needed the user fills it in and is responsible for it.
// Our own nodes can be listed too, with the address given by the user.

// manualPeerLimit is the list cap. A consortium has single digits to a few dozen nodes; more than
// that is misuse.
const manualPeerLimit = 100

// parseManualEnode parses enode://<pubkey>@<IP>:<port>[?discport=N] and returns the normalized
// enode (pubkey lowercased) and the pubkey. The host must be an IP other nodes can dial (see
// enode.ValidHostPort): Besu does not accept host names in an enode (the platform does not support
// --Xdns-enabled). ?discport is kept: the other side's UDP discovery port may differ from its TCP
// port, and the platform does not assume otherwise. The rules follow Besu 26.7.1's
// EnodeURLImpl.fromURI: the query can only be discport=<1-5 digits>; 0 = the other side has
// discovery off, 1-65535 = UDP port.
var discportRe = regexp.MustCompile(`^discport=([0-9]{1,5})$`)

// pubkeyHexRe matches an enode pubkey (64 bytes in hex, lowercase).
var pubkeyHexRe = regexp.MustCompile(`^[0-9a-f]{128}$`)

func parseManualEnode(raw string) (en, pub string, err error) {
	raw = strings.TrimSpace(raw)
	pub, err = enode.OnCurvePubKeyHex(raw)
	if err != nil {
		return "", "", err
	}
	hostPort, query, _ := strings.Cut(raw[strings.IndexByte(raw, '@')+1:], "?")
	if err := enode.ValidHostPort(hostPort); err != nil {
		return "", "", err
	}
	en = "enode://" + pub + "@" + hostPort
	if query != "" {
		m := discportRe.FindStringSubmatch(query)
		n := 0
		if m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		if m == nil || n > 65535 {
			return "", "", &enode.ParseError{Reason: api.ErrorReasonEnodeDiscport, Params: map[string]string{"value": strconv.Quote(query)}}
		}
		en += "?discport=" + strconv.Itoa(n)
	}
	return en, pub, nil
}

func (s *Server) CreateManualPeer(ctx context.Context, req api.CreateManualPeerRequestObject) (api.CreateManualPeerResponseObject, error) {
	if req.Body == nil {
		return api.CreateManualPeer400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing enode"}, nil // the contract's required check rejects this first; unreachable
	}
	en, pub, err := parseManualEnode(req.Body.Enode)
	if err != nil {
		return api.CreateManualPeer400JSONResponse(enodeError(err)), nil
	}
	m := store.ManualPeer{Pubkey: pub, Enode: en, CreatedAt: time.Now().UTC()}
	d := map[string]any{"pubkey": pub, "enode": en}
	if err := s.audit(ctx, "manual_peer_added", d); err != nil {
		return nil, err
	}
	added, full, err := s.store.AddManualPeer(ctx, m, manualPeerLimit)
	switch {
	case err != nil:
		s.auditFailedInternal(ctx, "manual_peer_added", d, err)
		return nil, err
	case full:
		p := map[string]string{"limit": strconv.Itoa(manualPeerLimit)}
		s.auditFailedReason(ctx, "manual_peer_added", d, api.ErrorReasonManualPeerListFull, p)
		return api.CreateManualPeer400JSONResponse(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonManualPeerListFull, p)), nil
	case !added:
		s.auditFailedReason(ctx, "manual_peer_added", d, api.ErrorReasonManualPeerExists, nil)
		return api.CreateManualPeer409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonManualPeerExists, nil)), nil
	}
	return api.CreateManualPeer201JSONResponse(api.ManualPeer{Pubkey: m.Pubkey, Address: pubAddress(m.Pubkey), Enode: m.Enode, CreatedAt: m.CreatedAt}), nil
}

func (s *Server) DeleteManualPeer(ctx context.Context, req api.DeleteManualPeerRequestObject) (api.DeleteManualPeerResponseObject, error) {
	pub := strings.ToLower(req.Pubkey)
	// Record first, then delete: a string that is not a pubkey cannot be in the list, so return 404
	// directly instead of writing arbitrary strings into the audit
	if !pubkeyHexRe.MatchString(pub) {
		return api.DeleteManualPeer404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonManualPeerNotFound, nil)), nil
	}
	d := map[string]any{"pubkey": pub}
	if err := s.audit(ctx, "manual_peer_deleted", d); err != nil {
		return nil, err
	}
	ok, err := s.store.DeleteManualPeer(ctx, pub)
	if err != nil {
		s.auditFailedInternal(ctx, "manual_peer_deleted", d, err)
		return nil, err
	}
	if !ok {
		s.auditFailedReason(ctx, "manual_peer_deleted", d, api.ErrorReasonManualPeerNotFound, nil)
		return api.DeleteManualPeer404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonManualPeerNotFound, nil)), nil
	}
	return api.DeleteManualPeer204Response{}, nil
}

// enodeError maps a bad enode format -> 400 with a reason; anything that is not a ParseError
// (should not happen) is passed through as is.
func enodeError(err error) api.Error {
	var pe *enode.ParseError
	if errors.As(err, &pe) {
		return apierr.New(api.ErrorCodeInvalid, pe.Reason, pe.Params)
	}
	return api.Error{Code: api.ErrorCodeInvalid, Message: err.Error()}
}
