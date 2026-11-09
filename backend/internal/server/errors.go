package server

import (
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

func errPtr(e api.Error) *api.Error { return &e }

// invalidMsg is a 400 unreachable over HTTP (the contract rejects it first); it keeps a Chinese
// message and carries no reason.
func invalidMsg(msg string) *api.Error { return &api.Error{Code: api.ErrorCodeInvalid, Message: msg} }

// invalidReason is a 400 with a reason.
func invalidReason(reason api.ErrorReason, params map[string]string) *api.Error {
	return errPtr(apierr.New(api.ErrorCodeInvalid, reason, params))
}

// causeError returns the underlying error's reason if it has one (the root cause, e.g. the node is
// unreachable); unrecognized errors use fallback, with the raw text in detail.
func causeError(code api.ErrorCode, fallback api.ErrorReason, err error) api.Error {
	if r, p, ok := apierr.ReasonOf(err); ok {
		return apierr.New(code, r, p)
	}
	return apierr.New(code, fallback, map[string]string{"detail": err.Error()})
}
