// Package probe implements the individual health checks. A probe knows how to
// ask one question of one upstream service and answer it as a Result. It knows
// nothing about storage, scheduling, or the lockout budget — those belong to
// the scheduler, which is the only component allowed to decide whether an
// expensive check runs at all.
package probe

import "context"

// State is a check's health as rendered on the status page.
type State string

const (
	StateUp       State = "up"
	StateDegraded State = "degraded"
	StateDown     State = "down"
	StateUnknown  State = "unknown"
	StateRetired  State = "retired"
)

// ReasonCode is a closed vocabulary describing why a check is not up.
//
// Probes never emit raw upstream text. SSO error pages routinely carry
// wstoken, passport, code, state, and password values in hidden inputs and
// query strings, and everything stored here is served by a public API — so
// sanitisation happens here, at the boundary, rather than being retrofitted
// downstream where one missed path is permanent.
type ReasonCode string

const (
	ReasonNone             ReasonCode = ""
	ReasonUnreachable      ReasonCode = "unreachable"
	ReasonTimeout          ReasonCode = "timeout"
	ReasonHTTP4xx          ReasonCode = "http_4xx"
	ReasonHTTP5xx          ReasonCode = "http_5xx"
	ReasonUnexpectedBody   ReasonCode = "unexpected_body"
	ReasonSSOFormMissing   ReasonCode = "sso_form_missing"
	ReasonSSOLoginRejected ReasonCode = "sso_login_rejected"
	ReasonSSOBridgeMissing ReasonCode = "sso_bridge_missing"
	ReasonTokenInvalid     ReasonCode = "token_invalid"
	ReasonUsernameMismatch ReasonCode = "username_mismatch"
	ReasonBreakerOpen      ReasonCode = "breaker_open"
	ReasonRetired          ReasonCode = "retired"
	ReasonLoginFormMissing ReasonCode = "login_form_missing"
	ReasonMXPartial        ReasonCode = "mx_partial"
	ReasonGateClosed       ReasonCode = "gate_closed"
	ReasonNotConfigured    ReasonCode = "not_configured"

	// ReasonSSORedirectFailed means credentials were submitted but the IdP
	// never handed the session back to the service.
	ReasonSSORedirectFailed ReasonCode = "sso_redirect_failed"
	// ReasonPausedAfterFailure means a credentialed check is suspended and its
	// last real attempt failed.
	ReasonPausedAfterFailure ReasonCode = "paused_after_failure"
)

// Result is one observation of one check.
type Result struct {
	State      State
	LatencyMS  int
	HTTPStatus int // 0 when no response was received
	ReasonCode ReasonCode
}

func up(latencyMS, status int) Result {
	return Result{State: StateUp, LatencyMS: latencyMS, HTTPStatus: status}
}

func down(latencyMS, status int, reason ReasonCode) Result {
	return Result{State: StateDown, LatencyMS: latencyMS, HTTPStatus: status, ReasonCode: reason}
}

// Probe asks one question of one service.
type Probe interface {
	Key() string
	Run(ctx context.Context) Result
}

// DeriveServiceState collapses a service's check states into the one shown on
// its row.
//
// A mix of healthy and failing checks is degraded, not down: "Moodle is
// reachable but you cannot log in" is a materially different message to a
// student than "Moodle is down", and flattening the two would make the page
// wrong in the case it exists to report.
//
// Retired and unknown checks are excluded rather than ranked. A retired API
// version is a deliberate end state, and an unknown check is missing data —
// counting either as evidence would let the page invent an outage or hide one.
func DeriveServiceState(states []State) State {
	if len(states) == 0 {
		return StateUnknown
	}

	live := make([]State, 0, len(states))
	for _, s := range states {
		if s != StateRetired {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return StateRetired
	}

	known := make([]State, 0, len(live))
	for _, s := range live {
		if s != StateUnknown {
			known = append(known, s)
		}
	}
	if len(known) == 0 {
		return StateUnknown
	}

	var ups, downs int
	for _, s := range known {
		switch s {
		case StateUp:
			ups++
		case StateDown:
			downs++
		}
	}
	switch {
	case downs == len(known):
		return StateDown
	case ups == len(known):
		return StateUp
	default:
		return StateDegraded
	}
}

// RollupState is what a check contributes to its service's row. A sign-in
// check suspended after a failure still counts as that failure: the pause
// exists to protect the account, and must not also be how an outage drops off
// the page.
func RollupState(s State, r ReasonCode) State {
	if s == StateUnknown && r == ReasonPausedAfterFailure {
		return StateDown
	}
	return s
}

// knownReasons is the closed set that may be published. Anything else is a
// programming error, and the API replaces it rather than passing it through:
// the whole point of the coded vocabulary is that no upstream text can ever
// reach a public response.
var knownReasons = map[ReasonCode]bool{
	ReasonNone: true, ReasonUnreachable: true, ReasonTimeout: true,
	ReasonHTTP4xx: true, ReasonHTTP5xx: true, ReasonUnexpectedBody: true,
	ReasonSSOFormMissing: true, ReasonSSOLoginRejected: true, ReasonSSOBridgeMissing: true,
	ReasonTokenInvalid: true, ReasonUsernameMismatch: true, ReasonBreakerOpen: true,
	ReasonGateClosed: true, ReasonNotConfigured: true, ReasonRetired: true,
	ReasonLoginFormMissing: true, ReasonMXPartial: true,
	ReasonSSORedirectFailed: true, ReasonPausedAfterFailure: true,
}

// IsKnownReason reports whether a reason code is safe to publish.
func IsKnownReason(r ReasonCode) bool { return knownReasons[r] }
