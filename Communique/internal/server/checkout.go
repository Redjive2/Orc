package server

import (
	"context"
	"net/http"
	"time"

	"orc/cq/internal/upgrade"
)

// `GET /api/v1/upgrade/checkout` — whether this machine could rebuild itself.
//
// Its own route rather than a field on the upgrade view, because it answers a
// different question at a different moment: that one reports what a build *did*, and
// this reports what one *would* do, before anybody commits to finding out. The
// browser polls it while `tooling › rebuild` is open.
//
// Read-only, and cheap enough to poll: a handful of `git rev-parse` and one
// `go version`, no fetch, nothing written. See upgrade/status.go for why not
// fetching is the right trade rather than a shortcut.
//
// Only this machine. The button also queues every agent machine, and the server
// cannot reach one to ask — that is the architecture, not an omission — so the page
// says whose checkout this is rather than letting a green light stand for a fleet.
func (s *Server) checkoutStatus(w http.ResponseWriter, r *http.Request) {
	got := s.checkCheckout(r)

	// A build already running is a stop, and the strongest one here. Two builds in
	// one checkout is one of them reading a tree the other is rewriting, and the
	// pull that starts the second would be fighting the compiler of the first for
	// the same files. It is also the state where the light is most likely to be
	// misread: a page polling through a ten-minute build sees a checkout that looks
	// perfectly fine, because it is.
	s.built.Lock()
	building := s.building
	s.built.Unlock()
	if building {
		got.Reasons = append([]upgrade.Reason{{
			Level: upgrade.Stop,
			Text:  "a build is already running on this machine",
			Fix:   "wait for it — the panel below says how it went",
		}}, got.Reasons...)
		got.Verdict = upgrade.Stop
	}

	// Whether it can restart is the server's own knowledge and not the checkout's,
	// so it is added here. A caution and not a stop: the build runs and lands the
	// new binaries, and what does not happen is this process becoming one of them.
	if s.restart == nil {
		got.Reasons = append(got.Reasons, upgrade.Reason{
			Level: upgrade.Caution,
			Text:  "this server is not supervised, so it cannot restart itself",
			Fix:   "run `cq serve` under a supervisor, or restart it by hand after the build",
		})
		if got.Verdict == upgrade.Go {
			got.Verdict = upgrade.Caution
		}
	}
	s.ok(w, r, got)
}

// CheckoutCacheFor is how long one inspection answers for.
//
// This route is polled by every open tab, and each inspection is six subprocesses.
// Four tabs left open over lunch is a git invocation a second, on a machine whose
// job is to serve mail — and none of what it measures changes that fast: a branch, a
// commit count, whether go is on the PATH.
//
// Short enough that a commit made in a terminal shows up in the time it takes to
// switch windows, which is the only interactivity anybody expects of it.
const CheckoutCacheFor = 5 * time.Second

// checkCheckout inspects the checkout, at most once per CheckoutCacheFor.
//
// The lock is held across the inspection on purpose, so ten tabs asking at once run
// one check and nine wait for it rather than ten racing into the same repository.
// That is a queue bounded by upgrade.CheckTimeout, which is why that constant is
// seconds rather than minutes.
//
// The request's own context is not used for the work. It would be the natural choice
// and it is the wrong one here: the first tab to give up would cancel an inspection
// the other nine are waiting on, and they would each start another. What that costs
// is an inspection that outlives its request by at most CheckTimeout.
func (s *Server) checkCheckout(r *http.Request) upgrade.Status {
	s.checked.Lock()
	defer s.checked.Unlock()

	if !s.checkedAt.IsZero() && s.now().Sub(s.checkedAt) < CheckoutCacheFor {
		return s.checkout
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), upgrade.CheckTimeout)
	defer cancel()

	s.checkout = s.upgrade.Check(ctx)
	s.checkedAt = s.now()
	return s.checkout
}
