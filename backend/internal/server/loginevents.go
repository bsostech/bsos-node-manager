package server

import (
	"context"
	"strconv"

	"besu-manager/backend/internal/api"
)

// loginEventsMax is the most entries returned at once; beyond that the result is truncated and the
// UI asks the user to narrow the range.
const loginEventsMax = 500

// ── GET /auth/login-events ──────────────────────────────────

func (s *Server) ListLoginEvents(ctx context.Context, req api.ListLoginEventsRequestObject) (api.ListLoginEventsResponseObject, error) {
	if req.Params.Since.IsZero() { // the generated code does not reject missing required parameters, so reject here: without them everything would be returned
		return api.ListLoginEvents400Response{}, nil
	}
	username, result := "", ""
	if req.Params.Username != nil {
		username = *req.Params.Username
	}
	if req.Params.Result != nil {
		if !req.Params.Result.Valid() {
			return api.ListLoginEvents400Response{}, nil
		}
		result = string(*req.Params.Result)
	}
	rows, err := s.store.ListLoginEvents(ctx, req.Params.Since, username, result, loginEventsMax+1) // fetch one extra to tell whether the limit is exceeded
	if err != nil {
		return nil, err
	}
	out := api.LoginEventList{Events: make([]api.LoginEvent, 0, len(rows)), Truncated: len(rows) > loginEventsMax}
	if out.Truncated {
		rows = rows[:loginEventsMax]
	}
	for _, e := range rows {
		ev := api.LoginEvent{Id: strconv.FormatInt(e.ID, 10), At: e.At.UTC(), Username: e.Username, Result: api.LoginResult(e.Result), SourceIp: e.SourceIP}
		if e.Reason != nil {
			r := api.LoginFailReason(*e.Reason)
			ev.Reason = &r
		}
		out.Events = append(out.Events, ev)
	}
	return api.ListLoginEvents200JSONResponse(out), nil
}
