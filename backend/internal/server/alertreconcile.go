package server

import (
	"context"
	"log"
	"time"

	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/store"
)

// alertStaleAfter: an alert firing in the database but absent from Prometheus for this long in a
// row is treated as ended. Far longer than the rule evaluation interval (15 s), so the gap right
// after a Prometheus restart before rules are evaluated is not closed by mistake.
const alertStaleAfter = 3 * time.Minute

// RunAlertReconcile: while monitoring is live (AlertsLive), each round compares firing alerts in
// the database with Prometheus's current alerts and ends the ones nobody will ever send a
// "resolved" for: alerts that clear within the grace period after a Prometheus restart get no
// resolved, and an Alertmanager restart forgets the alerts it held.
func (s *Server) RunAlertReconcile(ctx context.Context, interval time.Duration) {
	missing := map[store.AlertKey]time.Time{}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.prom.HasAlertmanager() || !s.prom.AlertsLive() { // no Alertmanager configured: alerts do not go through the platform webhook, nothing to reconcile
			clear(missing) // the truth is not visible during the outage, start counting again after recovery
			continue
		}
		if err := s.reconcileAlerts(ctx, missing, time.Now()); err != nil {
			log.Printf("alerts: reconcile: %v", err)
		}
	}
}

func (s *Server) reconcileAlerts(ctx context.Context, missing map[store.AlertKey]time.Time, now time.Time) error {
	active, err := s.prom.ActiveAlerts(ctx)
	if err != nil {
		return err
	}
	firing := "firing"
	open, err := s.store.ListAlerts(ctx, store.AlertFilter{Status: &firing})
	if err != nil {
		return err
	}
	stale := staleAlerts(open, active, missing, now)
	if len(stale) == 0 {
		return nil
	}
	var ids []string
	ended := make([]prom.EndedAlert, len(stale))
	for i, a := range stale {
		ids = append(ids, alertID(a))
		ended[i] = prom.EndedAlert{Labels: a.Labels, StartsAt: a.StartsAt}
	}
	const event = "alerts_auto_resolved"
	if err := s.audit(ctx, event, map[string]any{"alerts": ids}); err != nil {
		return err
	}
	// Tell Alertmanager it has ended first so its "which were notified" record updates; then write
	// the database ourselves: Alertmanager does not send "resolved" for a firing it never notified
	// (e.g. after a restart).
	if err := s.prom.EndAlerts(ctx, ended, now); err != nil {
		return err
	}
	for _, a := range stale {
		a.Status, a.EndsAt = "resolved", &now
		if err := s.store.UpsertAlert(ctx, a); err != nil {
			return err
		}
		delete(missing, store.AlertKey{Fingerprint: a.Fingerprint, StartsAt: a.StartsAt})
	}
	log.Printf("alerts: reconcile resolved %d alerts no longer firing: %v", len(ids), ids) // Core has no audit, so keep this log line
	return nil
}

// staleAlerts: entries in open with no match in active for alertStaleAfter in a row. missing holds
// the time each first failed to match, cleared when it matches.
func staleAlerts(open []store.Alert, active []map[string]string, missing map[store.AlertKey]time.Time, now time.Time) []store.Alert {
	var out []store.Alert
	seen := map[store.AlertKey]bool{}
	for _, a := range open {
		k := store.AlertKey{Fingerprint: a.Fingerprint, StartsAt: a.StartsAt}
		seen[k] = true
		if matchesAny(a.Labels, active) {
			delete(missing, k)
			continue
		}
		first, ok := missing[k]
		if !ok {
			missing[k] = now
			continue
		}
		if now.Sub(first) >= alertStaleAfter {
			out = append(out, a)
		}
	}
	for k := range missing {
		if !seen[k] {
			delete(missing, k)
		}
	}
	return out
}

// matchesAny: every label of the Prometheus alert appears in the database alert (Alertmanager may add external_labels).
func matchesAny(labels map[string]string, active []map[string]string) bool {
	for _, p := range active {
		ok := true
		for k, v := range p {
			if labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
