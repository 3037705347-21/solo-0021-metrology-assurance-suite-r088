package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrNotFound           = errors.New("resource not found")
	ErrDuplicateAssetTag  = errors.New("duplicate asset tag")
	ErrActiveCaseExists   = errors.New("device already has an unfinished case")
	ErrStateConflict      = errors.New("resource state conflict")
	ErrSealAlreadyCreated = errors.New("case already has an evidence seal")

	// ErrCaseAlreadyGrouped and ErrWorkOrderIncomplete are state conflicts that
	// keep their own identity while remaining mappable to the shared conflict
	// status used by the public API.
	ErrCaseAlreadyGrouped  = fmt.Errorf("%w: case already belongs to a work order", ErrStateConflict)
	ErrWorkOrderIncomplete = fmt.Errorf("%w: work order still has unfinished cases", ErrStateConflict)
)

func IsKnownError(err error) bool {
	return errors.Is(err, ErrInvalidInput) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrDuplicateAssetTag) ||
		errors.Is(err, ErrActiveCaseExists) ||
		errors.Is(err, ErrStateConflict) ||
		errors.Is(err, ErrSealAlreadyCreated)
}
