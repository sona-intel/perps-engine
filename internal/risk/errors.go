// Package risk holds the pure Perpl risk math, with no IO
package risk

import (
	"errors"
	"fmt"
)

// ErrInvalidInput marks data from the chain that the math cannot use, so it is a server side fault
var ErrInvalidInput = errors.New("risk: invalid input")

// ErrInvalidParam marks a caller supplied parameter outside its allowed range, so it maps to a 400
var ErrInvalidParam = errors.New("risk: invalid parameter")

const (
	MinBucketBps = 5
	MaxBucketBps = 500
	MinRangePct  = 1
	MaxRangePct  = 50
	MaxShock     = 0.5
)

type ParamError struct {
	Param  string
	Reason string
}

func (e *ParamError) Error() string {
	return fmt.Sprintf("risk: invalid %s: %s", e.Param, e.Reason)
}

func (e *ParamError) Is(target error) bool { return target == ErrInvalidParam }

func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}
