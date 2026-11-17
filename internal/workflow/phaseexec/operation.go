// Package phaseexec coordinates persistence around phase-owned operations.
// It does not choose work or interpret phase outcomes.
package phaseexec

import (
	"context"
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
)

type Change func(*work.State) (bool, error)

type Operation struct {
	Name    string
	Prepare func(*work.State) (Prepared, error)
}

type LockScope uint8

const (
	HoldForEffect LockScope = iota
	ReleaseForEffect
)

type Prepared struct {
	Changed bool
	Scope   LockScope
	// Reload adopts independent checkpoint writes even in a caller-held scope.
	Reload bool
	Run    func(context.Context) Result
}

type Result struct {
	Update Change
	Err    error
}

// ErrStaleInput refuses responses to replaced owners or changed state.
var ErrStaleInput = errors.New("workflow interaction is stale; inspect pending decisions before responding")

type InputID struct {
	Owner   work.AttemptRef
	Version work.StateVersion
	Kind    string
}

// Selection has exactly one alternative when constructed with Run, Await or Return.
// Operations and selections are transient values, never persisted plans.
type Selection[Q, O any] struct {
	kind      uint8
	operation Operation
	request   Q
	outcome   O
}

func Run[Q, O any](op Operation) Selection[Q, O]       { return Selection[Q, O]{kind: 1, operation: op} }
func Await[Q, O any](q Q) Selection[Q, O]              { return Selection[Q, O]{kind: 2, request: q} }
func Return[Q, O any](o O) Selection[Q, O]             { return Selection[Q, O]{kind: 3, outcome: o} }
func (s Selection[Q, O]) Operation() (Operation, bool) { return s.operation, s.kind == 1 }
func (s Selection[Q, O]) Input() (Q, bool)             { return s.request, s.kind == 2 }
func (s Selection[Q, O]) Outcome() (O, bool)           { return s.outcome, s.kind == 3 }
