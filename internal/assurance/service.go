package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			input.Nominal,
			input.Tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			"verification case opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			input.Observed,
			item.Nominal,
			item.Tolerance,
			input.Technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := item.RecordMeasurement(measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		detail := "measurement exceeded tolerance and requires another measurement"
		if measurement.Accepted {
			kind = "measurement_accepted"
			detail = "measurement is within tolerance and ready for review"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if err := item.Reopen(s.now()); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		actor := strings.TrimSpace(input.Actor)
		reason := strings.TrimSpace(input.Reason)
		if actor == "" || reason == "" {
			return fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			reason,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			seal.Summary,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) CreateWorkOrder(ctx context.Context, input contracts.CreateWorkOrderRequest) (contracts.WorkOrderResponse, error) {
	var result contracts.WorkOrderResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		item, err := domain.NewWorkOrder(
			m.NextID("workorder"),
			input.Customer,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutWorkOrder(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"work_order",
			item.ID,
			"",
			"work_order_opened",
			item.Steward,
			"customer work order opened",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = workOrderResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) AttachCase(ctx context.Context, workOrderID string, input contracts.AttachCaseRequest) (contracts.WorkOrderResponse, error) {
	id := strings.TrimSpace(workOrderID)
	caseID := strings.TrimSpace(input.CaseID)
	var result contracts.WorkOrderResponse
	// Grouping is a single atomic decision: the work order's member set, the
	// case's back-pointer, and the acceptance event are only ever committed
	// together. Any validation failure returns before the first write, so a
	// rejected grouping leaves no member, binding, or audit trace behind.
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		order, exists := m.WorkOrder(id)
		if !exists {
			return fmt.Errorf("%w: work order %s", domain.ErrNotFound, id)
		}
		// A closed commission can never take on new work.
		if !order.IsOpen() {
			return fmt.Errorf("%w: work order %s is %s", domain.ErrStateConflict, order.ID, order.State)
		}
		item, exists := m.Case(caseID)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, caseID)
		}
		// The case's back-pointer is the authority for its one membership.
		// Checking it here, inside the same commit that sets it, makes a
		// concurrent grouping of the same case lose cleanly instead of
		// leaving the case visible in two commissions.
		if item.WorkOrderID != "" {
			if item.WorkOrderID == order.ID {
				return fmt.Errorf("%w: case %s is already attached to work order %s", domain.ErrStateConflict, item.ID, order.ID)
			}
			return fmt.Errorf("%w: case %s already belongs to work order %s", domain.ErrCaseAlreadyGrouped, item.ID, item.WorkOrderID)
		}
		if err := order.AttachCase(item.ID, s.now()); err != nil {
			return err
		}
		if err := item.BindWorkOrder(order.ID, s.now()); err != nil {
			return err
		}
		if err := m.UpdateWorkOrder(order); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"work_order",
			order.ID,
			item.ID,
			"case_attached",
			order.Steward,
			"case attached to customer work order",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = workOrderResponse(m, order)
		return nil
	})
	return result, err
}

func (s *Service) CloseWorkOrder(ctx context.Context, workOrderID string) (contracts.WorkOrderResponse, error) {
	id := strings.TrimSpace(workOrderID)
	var result contracts.WorkOrderResponse
	// Closing and the completeness gate run in one committed transaction.
	// Re-reading the authoritative member states here closes the
	// check-then-act window: a case attached (or reworked) concurrently is
	// observed under the same lock that performs the close, so the order can
	// never end up closed while it still holds an unsealed case.
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		order, exists := m.WorkOrder(id)
		if !exists {
			return fmt.Errorf("%w: work order %s", domain.ErrNotFound, id)
		}
		if !order.IsOpen() {
			return fmt.Errorf("%w: work order %s is already %s", domain.ErrStateConflict, order.ID, order.State)
		}
		if !workOrderComplete(m, order) {
			return fmt.Errorf("%w: work order %s", domain.ErrWorkOrderIncomplete, id)
		}
		if err := order.Close(s.now()); err != nil {
			return err
		}
		if err := m.UpdateWorkOrder(order); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"work_order",
			order.ID,
			"",
			"work_order_closed",
			order.Steward,
			"customer work order closed",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = workOrderResponse(m, order)
		return nil
	})
	return result, err
}

func (s *Service) WorkOrder(ctx context.Context, id string) (contracts.WorkOrderResponse, error) {
	var result contracts.WorkOrderResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		order, exists := snapshot.WorkOrder(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: work order %s", domain.ErrNotFound, id)
		}
		result = workOrderResponseFromSnapshot(snapshot, order)
		return nil
	})
	return result, err
}

type workOrderSource interface {
	Case(id string) (domain.VerificationCase, bool)
}

// workOrderComplete reports whether the work order is genuinely finished.
// The case states are the authoritative fact: an order is complete only when
// it has at least one member and every member is sealed. Merely holding a
// measurement, still measuring, awaiting review, or awaiting rework all leave
// the order open.
func workOrderComplete(source workOrderSource, order domain.WorkOrder) bool {
	if len(order.CaseIDs) == 0 {
		return false
	}
	for _, caseID := range order.CaseIDs {
		item, exists := source.Case(caseID)
		if !exists || item.State != domain.CaseSealed {
			return false
		}
	}
	return true
}

func workOrderResponse(m *repository.Mutation, order domain.WorkOrder) contracts.WorkOrderResponse {
	cases := make([]domain.VerificationCase, 0, len(order.CaseIDs))
	for _, caseID := range order.CaseIDs {
		if item, exists := m.Case(caseID); exists {
			cases = append(cases, item)
		}
	}
	return contracts.WorkOrderResponse{
		WorkOrder: order,
		Cases:     cases,
		Complete:  workOrderComplete(m, order),
		Events:    m.WorkOrderEvents(order.ID),
	}
}

func workOrderResponseFromSnapshot(snapshot repository.Snapshot, order domain.WorkOrder) contracts.WorkOrderResponse {
	cases := make([]domain.VerificationCase, 0, len(order.CaseIDs))
	for _, caseID := range order.CaseIDs {
		if item, exists := snapshot.Case(caseID); exists {
			cases = append(cases, item)
		}
	}
	return contracts.WorkOrderResponse{
		WorkOrder: order,
		Cases:     cases,
		Complete:  workOrderComplete(snapshot, order),
		Events:    snapshot.WorkOrderEvents(order.ID),
	}
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
