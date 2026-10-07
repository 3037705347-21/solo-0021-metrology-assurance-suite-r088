package assurance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

func newTestService() *Service {
	return NewService(repository.New())
}

func mustDevice(t *testing.T, s *Service, tag string) string {
	t.Helper()
	device, err := s.RegisterDevice(context.Background(), contracts.RegisterDeviceRequest{
		AssetTag: tag,
		Model:    "Bench Meter",
		Room:     "Room A",
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	return device.ID
}

// openCase opens a fresh case in awaiting_measurement.
func openCase(t *testing.T, s *Service, deviceID string) domain.VerificationCase {
	t.Helper()
	view, err := s.OpenCase(context.Background(), contracts.OpenCaseRequest{
		DeviceID:  deviceID,
		Nominal:   25.0,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	return view.Case
}

// sealedCase registers a device, opens a case, records an in-tolerance
// measurement, and seals it, returning the sealed case.
func sealedCase(t *testing.T, s *Service, tag string) domain.VerificationCase {
	t.Helper()
	deviceID := mustDevice(t, s, tag)
	item := openCase(t, s, deviceID)
	if _, err := s.RecordMeasurement(context.Background(), item.ID, contracts.RecordMeasurementRequest{
		Observed:   25.1,
		Technician: "Rui",
	}); err != nil {
		t.Fatalf("record measurement: %v", err)
	}
	view, err := s.SealCase(context.Background(), item.ID, contracts.SealCaseRequest{
		Reviewer: "Verifier",
		Summary:  "accepted measurement sealed",
	})
	if err != nil {
		t.Fatalf("seal case: %v", err)
	}
	return view.Case
}

// measuringCase returns a case that has only been opened and is still awaiting
// measurement.
func measuringCase(t *testing.T, s *Service, tag string) domain.VerificationCase {
	t.Helper()
	deviceID := mustDevice(t, s, tag)
	return openCase(t, s, deviceID)
}

// recheckCase returns a case that recorded an out-of-tolerance measurement and
// is awaiting rework.
func recheckCase(t *testing.T, s *Service, tag string) domain.VerificationCase {
	t.Helper()
	deviceID := mustDevice(t, s, tag)
	item := openCase(t, s, deviceID)
	view, err := s.RecordMeasurement(context.Background(), item.ID, contracts.RecordMeasurementRequest{
		Observed:   26.5,
		Technician: "Rui",
	})
	if err != nil {
		t.Fatalf("record out-of-tolerance measurement: %v", err)
	}
	return view.Case
}

func mustWorkOrder(t *testing.T, s *Service, customer string) domain.WorkOrder {
	t.Helper()
	view, err := s.CreateWorkOrder(context.Background(), contracts.CreateWorkOrderRequest{
		Customer: customer,
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("create work order: %v", err)
	}
	return view.WorkOrder
}

func mustAttach(t *testing.T, s *Service, orderID, caseID string) contracts.WorkOrderResponse {
	t.Helper()
	view, err := s.AttachCase(context.Background(), orderID, contracts.AttachCaseRequest{CaseID: caseID})
	if err != nil {
		t.Fatalf("attach case %s to %s: %v", caseID, orderID, err)
	}
	return view
}

func eventKinds(events []domain.AuditEvent) map[string]int {
	kinds := make(map[string]int)
	for _, event := range events {
		kinds[event.Kind]++
	}
	return kinds
}

func TestAttachBindsOrderCaseAndEventAtomically(t *testing.T) {
	s := newTestService()
	order := mustWorkOrder(t, s, "Acme")
	item := sealedCase(t, s, "MET-2001")

	view := mustAttach(t, s, order.ID, item.ID)
	if len(view.WorkOrder.CaseIDs) != 1 || view.WorkOrder.CaseIDs[0] != item.ID {
		t.Fatalf("order members = %v, want [%s]", view.WorkOrder.CaseIDs, item.ID)
	}
	if got := view.Cases[0].WorkOrderID; got != order.ID {
		t.Fatalf("case back-pointer = %q, want %q", got, order.ID)
	}
	if eventKinds(view.Events)["case_attached"] != 1 {
		t.Fatalf("case_attached events = %v, want exactly one", view.Events)
	}

	// The authoritative case record also reflects the single membership.
	caseView, err := s.Case(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("read case: %v", err)
	}
	if caseView.Case.WorkOrderID != order.ID {
		t.Fatalf("stored case work order = %q, want %q", caseView.Case.WorkOrderID, order.ID)
	}
}

func TestCloseRequiresEveryMemberSealed(t *testing.T) {
	s := newTestService()

	cases := []struct {
		name string
		item domain.VerificationCase
	}{
		{"measuring", measuringCase(t, s, "MET-2101")},
		{"recheck", recheckCase(t, s, "MET-2102")},
	}

	for index, tc := range cases {
		t.Run(tc.name+" blocks completion", func(t *testing.T) {
			order := mustWorkOrder(t, s, "Acme")
			sealedTag := "MET-210" + string(rune('3'+index))
			mustAttach(t, s, order.ID, sealedCase(t, s, sealedTag).ID)
			mustAttach(t, s, order.ID, tc.item.ID)

			before, err := s.WorkOrder(context.Background(), order.ID)
			if err != nil {
				t.Fatalf("read order: %v", err)
			}
			if before.Complete {
				t.Fatal("order with an unfinished member reported complete")
			}
			if _, err := s.CloseWorkOrder(context.Background(), order.ID); !errors.Is(err, domain.ErrWorkOrderIncomplete) {
				t.Fatalf("close error = %v, want ErrWorkOrderIncomplete", err)
			}
			after, _ := s.WorkOrder(context.Background(), order.ID)
			if after.WorkOrder.State != domain.WorkOrderOpen {
				t.Fatalf("order state = %q, want open", after.WorkOrder.State)
			}
			if eventKinds(after.Events)["work_order_closed"] != 0 {
				t.Fatal("failed close left a work_order_closed event")
			}
		})
	}

	// With every member sealed the order closes exactly once.
	order := mustWorkOrder(t, s, "Acme")
	sealedA := sealedCase(t, s, "MET-2110")
	sealedB := sealedCase(t, s, "MET-2111")
	mustAttach(t, s, order.ID, sealedA.ID)
	mustAttach(t, s, order.ID, sealedB.ID)

	view, err := s.CloseWorkOrder(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("close complete order: %v", err)
	}
	if !view.Complete || view.WorkOrder.State != domain.WorkOrderClosed {
		t.Fatalf("closed order = state %q complete %v", view.WorkOrder.State, view.Complete)
	}
	if _, err := s.CloseWorkOrder(context.Background(), order.ID); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("second close error = %v, want state conflict", err)
	}
}

func TestEmptyWorkOrderCannotClose(t *testing.T) {
	s := newTestService()
	order := mustWorkOrder(t, s, "Acme")
	if _, err := s.CloseWorkOrder(context.Background(), order.ID); !errors.Is(err, domain.ErrWorkOrderIncomplete) {
		t.Fatalf("close empty order error = %v, want incomplete", err)
	}
}

func TestCaseBelongsToExactlyOneWorkOrder(t *testing.T) {
	s := newTestService()
	first := mustWorkOrder(t, s, "Acme")
	second := mustWorkOrder(t, s, "Beta")
	item := sealedCase(t, s, "MET-2201")

	mustAttach(t, s, first.ID, item.ID)
	_, err := s.AttachCase(context.Background(), second.ID, contracts.AttachCaseRequest{CaseID: item.ID})
	if !errors.Is(err, domain.ErrCaseAlreadyGrouped) {
		t.Fatalf("second grouping error = %v, want ErrCaseAlreadyGrouped", err)
	}

	firstView, _ := s.WorkOrder(context.Background(), first.ID)
	secondView, _ := s.WorkOrder(context.Background(), second.ID)
	if len(firstView.WorkOrder.CaseIDs) != 1 {
		t.Fatalf("first order members = %v", firstView.WorkOrder.CaseIDs)
	}
	if len(secondView.WorkOrder.CaseIDs) != 0 {
		t.Fatalf("rejected grouping left members: %v", secondView.WorkOrder.CaseIDs)
	}
	if eventKinds(secondView.Events)["case_attached"] != 0 {
		t.Fatal("rejected grouping left a case_attached event")
	}
	caseView, _ := s.Case(context.Background(), item.ID)
	if caseView.Case.WorkOrderID != first.ID {
		t.Fatalf("case membership = %q, want %q", caseView.Case.WorkOrderID, first.ID)
	}

	// Re-attaching to the same order is an idempotent conflict, not a
	// duplicate member.
	if _, err := s.AttachCase(context.Background(), first.ID, contracts.AttachCaseRequest{CaseID: item.ID}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("duplicate same-order attach error = %v, want state conflict", err)
	}
	firstView, _ = s.WorkOrder(context.Background(), first.ID)
	if len(firstView.WorkOrder.CaseIDs) != 1 {
		t.Fatalf("duplicate attach created members: %v", firstView.WorkOrder.CaseIDs)
	}
}

func TestClosedWorkOrderRejectsNewMember(t *testing.T) {
	s := newTestService()
	order := mustWorkOrder(t, s, "Acme")
	mustAttach(t, s, order.ID, sealedCase(t, s, "MET-2301").ID)
	if _, err := s.CloseWorkOrder(context.Background(), order.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	later := sealedCase(t, s, "MET-2302")
	_, err := s.AttachCase(context.Background(), order.ID, contracts.AttachCaseRequest{CaseID: later.ID})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("attach after close error = %v, want state conflict", err)
	}

	view, _ := s.WorkOrder(context.Background(), order.ID)
	if len(view.WorkOrder.CaseIDs) != 1 {
		t.Fatalf("closed order gained a member: %v", view.WorkOrder.CaseIDs)
	}
	caseView, _ := s.Case(context.Background(), later.ID)
	if caseView.Case.WorkOrderID != "" {
		t.Fatalf("rejected case was bound to closed order: %q", caseView.Case.WorkOrderID)
	}
}

func TestRejectedAttachLeavesNoTrace(t *testing.T) {
	s := newTestService()
	order := mustWorkOrder(t, s, "Acme")

	// Nonexistent case.
	if _, err := s.AttachCase(context.Background(), order.ID, contracts.AttachCaseRequest{CaseID: "case-9999"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("attach missing case error = %v, want not found", err)
	}
	view, _ := s.WorkOrder(context.Background(), order.ID)
	if len(view.WorkOrder.CaseIDs) != 0 || len(view.Events) != 1 {
		t.Fatalf("rejected attach left state: members=%v events=%d", view.WorkOrder.CaseIDs, len(view.Events))
	}
	// The only surviving event is the work order's own opening event.
	if eventKinds(view.Events)["case_attached"] != 0 {
		t.Fatal("rejected attach left a case_attached event")
	}
}

// TestConcurrentAttachAndCloseConverges races a case attachment against a
// close of the same order. The order must always end in one of the two
// consistent outcomes: closed with every member sealed, or still open because
// it picked up an unfinished member. It must never be closed while holding an
// unfinished case.
func TestConcurrentAttachAndCloseConverges(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		s := newTestService()
		order := mustWorkOrder(t, s, "Acme")
		mustAttach(t, s, order.ID, sealedCase(t, s, "MET-2400").ID)

		// A brand new, still-measuring case races against the close.
		deviceID := mustDevice(t, s, "MET-2499")
		pending := openCase(t, s, deviceID)

		var attachErr, closeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, attachErr = s.AttachCase(context.Background(), order.ID, contracts.AttachCaseRequest{CaseID: pending.ID})
		}()
		go func() {
			defer wg.Done()
			_, closeErr = s.CloseWorkOrder(context.Background(), order.ID)
		}()
		wg.Wait()

		view, err := s.WorkOrder(context.Background(), order.ID)
		if err != nil {
			t.Fatalf("read order: %v", err)
		}
		member := make(map[string]bool)
		for _, id := range view.WorkOrder.CaseIDs {
			member[id] = true
		}

		switch view.WorkOrder.State {
		case domain.WorkOrderClosed:
			if closeErr != nil {
				t.Fatalf("order closed but close returned %v", closeErr)
			}
			if member[pending.ID] {
				t.Fatalf("iteration %d: closed order holds the unfinished case", iteration)
			}
			if !errors.Is(attachErr, domain.ErrStateConflict) {
				t.Fatalf("iteration %d: attach against closed order err = %v", iteration, attachErr)
			}
			for _, item := range view.Cases {
				if item.State != domain.CaseSealed {
					t.Fatalf("iteration %d: closed order has unsealed member %s (%s)", iteration, item.ID, item.State)
				}
			}
		case domain.WorkOrderOpen:
			if !member[pending.ID] {
				t.Fatalf("iteration %d: order open but pending case is not a member", iteration)
			}
			if !errors.Is(closeErr, domain.ErrWorkOrderIncomplete) {
				t.Fatalf("iteration %d: open order after race, close err = %v", iteration, closeErr)
			}
		default:
			t.Fatalf("iteration %d: unexpected order state %q", iteration, view.WorkOrder.State)
		}

		// The pending case is either unbound or bound to exactly this order.
		caseView, _ := s.Case(context.Background(), pending.ID)
		if caseView.Case.WorkOrderID != "" && caseView.Case.WorkOrderID != order.ID {
			t.Fatalf("iteration %d: pending case bound to %q", iteration, caseView.Case.WorkOrderID)
		}
		if eventKinds(view.Events)["work_order_closed"] > 1 {
			t.Fatalf("iteration %d: multiple close events", iteration)
		}
	}
}

// TestConcurrentAttachSameCaseConverges races grouping one case into two
// different orders. Exactly one grouping may win; the case must appear in one
// order only and hold one back-pointer.
func TestConcurrentAttachSameCaseConverges(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		s := newTestService()
		first := mustWorkOrder(t, s, "Acme")
		second := mustWorkOrder(t, s, "Beta")
		item := sealedCase(t, s, "MET-2500")

		var firstErr, secondErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, firstErr = s.AttachCase(context.Background(), first.ID, contracts.AttachCaseRequest{CaseID: item.ID})
		}()
		go func() {
			defer wg.Done()
			_, secondErr = s.AttachCase(context.Background(), second.ID, contracts.AttachCaseRequest{CaseID: item.ID})
		}()
		wg.Wait()

		if (firstErr == nil) == (secondErr == nil) {
			t.Fatalf("iteration %d: firstErr=%v secondErr=%v, want exactly one success", iteration, firstErr, secondErr)
		}
		if secondErr != nil && !errors.Is(secondErr, domain.ErrCaseAlreadyGrouped) {
			t.Fatalf("iteration %d: losing attach err = %v", iteration, secondErr)
		}
		if firstErr != nil && !errors.Is(firstErr, domain.ErrCaseAlreadyGrouped) {
			t.Fatalf("iteration %d: losing attach err = %v", iteration, firstErr)
		}

		firstView, _ := s.WorkOrder(context.Background(), first.ID)
		secondView, _ := s.WorkOrder(context.Background(), second.ID)
		inFirst := len(firstView.WorkOrder.CaseIDs) == 1 && firstView.WorkOrder.CaseIDs[0] == item.ID
		inSecond := len(secondView.WorkOrder.CaseIDs) == 1 && secondView.WorkOrder.CaseIDs[0] == item.ID
		if inFirst == inSecond {
			t.Fatalf("iteration %d: case in first=%v second=%v", iteration, inFirst, inSecond)
		}
		attachedEvents := eventKinds(firstView.Events)["case_attached"] + eventKinds(secondView.Events)["case_attached"]
		if attachedEvents != 1 {
			t.Fatalf("iteration %d: case_attached events = %d, want 1", iteration, attachedEvents)
		}
		caseView, _ := s.Case(context.Background(), item.ID)
		want := first.ID
		if inSecond {
			want = second.ID
		}
		if caseView.Case.WorkOrderID != want {
			t.Fatalf("iteration %d: case back-pointer = %q, want %q", iteration, caseView.Case.WorkOrderID, want)
		}
	}
}
