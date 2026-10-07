package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
)

type VerificationCase struct {
	ID          string     `json:"id"`
	DeviceID    string     `json:"device_id"`
	WorkOrderID string     `json:"work_order_id,omitempty"`
	Nominal     float64    `json:"nominal"`
	Tolerance   float64    `json:"tolerance"`
	Steward     string     `json:"steward"`
	State       CaseState  `json:"state"`
	Version     int        `json:"version"`
	OpenedAt    time.Time  `json:"opened_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	SealedAt    *time.Time `json:"sealed_at,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:        strings.TrimSpace(id),
		DeviceID:  strings.TrimSpace(deviceID),
		Nominal:   nominal,
		Tolerance: tolerance,
		Steward:   strings.TrimSpace(steward),
		State:     CaseAwaitingMeasurement,
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

func (c *VerificationCase) RecordMeasurement(accepted bool, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	if accepted {
		c.State = CaseReviewReady
	} else {
		c.State = CaseRecheckRequired
	}
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Reopen(at time.Time) error {
	if c.State != CaseRecheckRequired {
		return fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Seal(at time.Time) error {
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: case is not ready for review", ErrStateConflict)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

// BindWorkOrder attaches the case to exactly one customer work order. A case
// that already belongs to a work order must keep its original membership so a
// later attribution can never make it appear in two commissions at once.
func (c *VerificationCase) BindWorkOrder(workOrderID string, at time.Time) error {
	if c.WorkOrderID != "" {
		return fmt.Errorf(
			"%w: case %s already belongs to work order %s",
			ErrCaseAlreadyGrouped,
			c.ID,
			c.WorkOrderID,
		)
	}
	target := strings.TrimSpace(workOrderID)
	if target == "" {
		return fmt.Errorf("%w: work order id is required", ErrInvalidInput)
	}
	c.WorkOrderID = target
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

type WorkOrderState string

const (
	WorkOrderOpen   WorkOrderState = "open"
	WorkOrderClosed WorkOrderState = "closed"
)

// WorkOrder groups the verification cases that belong to one customer
// commission. Its member set is the record the laboratory reads back when it
// reports progress to a customer.
type WorkOrder struct {
	ID        string         `json:"id"`
	Customer  string         `json:"customer"`
	Steward   string         `json:"steward"`
	State     WorkOrderState `json:"state"`
	CaseIDs   []string       `json:"case_ids"`
	Version   int            `json:"version"`
	OpenedAt  time.Time      `json:"opened_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	ClosedAt  *time.Time     `json:"closed_at,omitempty"`
}

func NewWorkOrder(id, customer, steward string, at time.Time) (WorkOrder, error) {
	item := WorkOrder{
		ID:        strings.TrimSpace(id),
		Customer:  strings.TrimSpace(customer),
		Steward:   strings.TrimSpace(steward),
		State:     WorkOrderOpen,
		CaseIDs:   []string{},
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return WorkOrder{}, err
	}
	return item, nil
}

func (w WorkOrder) Validate() error {
	if w.ID == "" || w.Customer == "" || w.Steward == "" {
		return fmt.Errorf("%w: work order id, customer, and steward are required", ErrInvalidInput)
	}
	if w.Version < 1 || w.OpenedAt.IsZero() || w.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: work order version and timestamps are required", ErrInvalidInput)
	}
	switch w.State {
	case WorkOrderOpen, WorkOrderClosed:
	default:
		return fmt.Errorf("%w: unsupported work order state %q", ErrInvalidInput, w.State)
	}
	for _, caseID := range w.CaseIDs {
		if strings.TrimSpace(caseID) == "" {
			return fmt.Errorf("%w: work order case ids must be non-empty", ErrInvalidInput)
		}
	}
	return nil
}

func (w WorkOrder) IsOpen() bool {
	return w.State == WorkOrderOpen
}

func (w WorkOrder) HasCase(caseID string) bool {
	target := strings.TrimSpace(caseID)
	for _, item := range w.CaseIDs {
		if item == target {
			return true
		}
	}
	return false
}

func (w *WorkOrder) AttachCase(caseID string, at time.Time) error {
	if w.State != WorkOrderOpen {
		return fmt.Errorf("%w: work order %s is %s", ErrStateConflict, w.ID, w.State)
	}
	target := strings.TrimSpace(caseID)
	if target == "" {
		return fmt.Errorf("%w: case id is required", ErrInvalidInput)
	}
	if w.HasCase(target) {
		return fmt.Errorf("%w: case %s is already attached to work order %s", ErrStateConflict, target, w.ID)
	}
	w.CaseIDs = append(w.CaseIDs, target)
	w.Version++
	w.UpdatedAt = at.UTC()
	return nil
}

func (w *WorkOrder) Close(at time.Time) error {
	if w.State != WorkOrderOpen {
		return fmt.Errorf("%w: work order %s is already %s", ErrStateConflict, w.ID, w.State)
	}
	stamp := at.UTC()
	w.State = WorkOrderClosed
	w.Version++
	w.UpdatedAt = stamp
	w.ClosedAt = &stamp
	return nil
}
