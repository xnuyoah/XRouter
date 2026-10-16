package common

import (
	"fmt"
	"net/url"
	"strings"
)

// UnsupportedPartError is a request-scoped rejection. The part type was present
// and this translation has no equivalent, so the request must not be sent with
// that part removed.
type UnsupportedPartError struct {
	Type string
}

func (e *UnsupportedPartError) Error() string {
	if e == nil || e.Type == "" {
		return "unsupported content part"
	}
	return fmt.Sprintf("unsupported content part: %s", e.Type)
}

func (e *UnsupportedPartError) StatusCode() int { return 400 }

func (e *UnsupportedPartError) IsRequestScoped() bool { return true }

// UserTurnDrops is the shared policy for a part a translation cannot send: text
// or any other sendable part beside it still goes out, but a user turn left with
// nothing to send is refused rather than forwarded empty. The decision is made
// per user turn, so earlier or later turns, system prompts and developer
// prompts can never hide an emptied user turn.
type UserTurnDrops struct {
	turn  string
	first string
}

// Drop records a part of the current user turn that cannot be sent. The first
// type recorded within a turn is the one reported.
func (d *UserTurnDrops) Drop(partType string) {
	if d.turn == "" {
		d.turn = partType
	}
}

// EndTurn closes the current user turn. sendable is the number of parts the turn
// contributed to the translated request. A turn that dropped a part and
// contributed nothing is remembered; a later turn never clears it.
func (d *UserTurnDrops) EndTurn(sendable int) {
	if d.turn != "" && sendable <= 0 && d.first == "" {
		d.first = d.turn
	}
	d.turn = ""
}

// Err returns the refusal for the first emptied user turn, or nil.
func (d *UserTurnDrops) Err() error {
	if d.first == "" {
		return nil
	}
	return &UnsupportedPartError{Type: d.first}
}

// UserRun follows the consecutive user content that a target merges into one
// user turn. Add records content that keeps the turn alive, Drop records a part
// that could not be sent, and End closes the turn when model content starts or
// the input ends. Every method is a no-op on a nil receiver, so callers can pass
// nil for content that is not user content.
type UserRun struct {
	drops    UserTurnDrops
	sendable int
}

// Add records one sendable item in the current user turn.
func (r *UserRun) Add() {
	if r != nil {
		r.sendable++
	}
}

// Drop records a part of the current user turn that cannot be sent.
func (r *UserRun) Drop(partType string) {
	if r != nil {
		r.drops.Drop(partType)
	}
}

// End closes the current user turn.
func (r *UserRun) End() {
	if r != nil {
		r.drops.EndTurn(r.sendable)
		r.sendable = 0
	}
}

// Err returns the refusal for the first emptied user turn, or nil.
func (r *UserRun) Err() error {
	if r == nil {
		return nil
	}
	return r.drops.Err()
}

// IsHTTPURL reports whether value is an absolute http or https URL with a host.
func IsHTTPURL(value string) bool {
	parsed, errParse := url.Parse(strings.TrimSpace(value))
	if errParse != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
