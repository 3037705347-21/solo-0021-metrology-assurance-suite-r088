package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type raceOutcome struct {
	kind     string
	response apiResponse
	err      error
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"group-work-order":         checkGroupWorkOrder,
		"work-order-race":          checkWorkOrderRace,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

// checkGroupWorkOrder drives the full commission lifecycle: grouping, sealing
// every member, and only then closing. It also proves that rejected groupings
// leave no member or acceptance trace behind.
func checkGroupWorkOrder() error {
	h := newHarness()
	defer h.close()

	workOrderID, err := h.createWorkOrder("Acme Lab", "Lin")
	if err != nil {
		return err
	}

	firstID, err := h.openReadyCase("MET-1501")
	if err != nil {
		return err
	}
	secondID, err := h.openAwaitingCase("MET-1502")
	if err != nil {
		return err
	}
	thirdID, err := h.openRecheckCase("MET-1503")
	if err != nil {
		return err
	}
	looseID, err := h.openAwaitingCase("MET-1504")
	if err != nil {
		return err
	}

	for _, caseID := range []string{firstID, secondID, thirdID} {
		if err := h.attachCase(workOrderID, caseID); err != nil {
			return err
		}
	}

	// The commission stays open while members are still measured or in recheck.
	view, err := h.getWorkOrder(workOrderID)
	if err != nil {
		return err
	}
	complete, err := nestedBool(view.Body, "complete")
	if err != nil {
		return err
	}
	if complete {
		return errors.New("work order reads complete while members are not sealed")
	}
	state, err := nestedString(view.Body, "work_order", "state")
	if err != nil {
		return err
	}
	if state != "open" {
		return fmt.Errorf("work order state before completion = %q", state)
	}

	rejected, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/close", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(rejected, "state_conflict"); err != nil {
		return err
	}
	stillOpen, err := h.getWorkOrder(workOrderID)
	if err != nil {
		return err
	}
	openState, err := nestedString(stillOpen.Body, "work_order", "state")
	if err != nil {
		return err
	}
	if openState != "open" {
		return fmt.Errorf("rejected close changed order state to %q", openState)
	}
	if rejectedCloses, err := countEvents(stillOpen.Body, "work_order_closed", ""); err != nil || rejectedCloses != 0 {
		return fmt.Errorf("rejected close left %d close events", rejectedCloses)
	}

	// Re-attaching the same case must be rejected without a second trace.
	dup, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/cases", map[string]any{
		"case_id": firstID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(dup, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(dup, "state_conflict"); err != nil {
		return err
	}
	view, err = h.getWorkOrder(workOrderID)
	if err != nil {
		return err
	}
	members, err := nestedSlicePath(view.Body, "work_order", "case_ids")
	if err != nil {
		return err
	}
	if len(members) != 3 {
		return fmt.Errorf("rejected attachment changed membership: %d members", len(members))
	}
	dupEvents, err := countEvents(view.Body, "case_attached", firstID)
	if err != nil {
		return err
	}
	if dupEvents != 1 {
		return fmt.Errorf("rejected re-attach left %d acceptance events for the case", dupEvents)
	}

	// A case already grouped into one commission cannot join another one.
	otherID, err := h.createWorkOrder("Beta Lab", "Mira")
	if err != nil {
		return err
	}
	moved, err := h.request(http.MethodPost, "/workorders/"+otherID+"/cases", map[string]any{
		"case_id": firstID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(moved, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(moved, "state_conflict"); err != nil {
		return err
	}
	other, err := h.getWorkOrder(otherID)
	if err != nil {
		return err
	}
	otherMembers, err := nestedSlicePath(other.Body, "work_order", "case_ids")
	if err != nil {
		return err
	}
	if len(otherMembers) != 0 {
		return errors.New("rejected cross-order attribution left a member behind")
	}
	otherEvents, err := nestedSlice(other.Body, "events")
	if err != nil {
		return err
	}
	for _, item := range otherEvents {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == "case_attached" {
			return errors.New("rejected cross-order attribution left an acceptance event behind")
		}
	}
	caseView, err := h.request(http.MethodGet, "/cases/"+firstID, nil)
	if err != nil {
		return err
	}
	bound, err := nestedString(caseView.Body, "case", "work_order_id")
	if err != nil {
		return err
	}
	if bound != workOrderID {
		return fmt.Errorf("case bound to %q after rejected move", bound)
	}

	// A missing case cannot be accepted into a commission either.
	missing, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/cases", map[string]any{
		"case_id": "case-9999",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusNotFound); err != nil {
		return err
	}
	view, err = h.getWorkOrder(workOrderID)
	if err != nil {
		return err
	}
	if attached, err := countEvents(view.Body, "case_attached", ""); err != nil || attached != 3 {
		return fmt.Errorf("missing-case rejection left %d acceptance events", attached)
	}

	// Finish every member: recheck case reopens and gets measured first.
	reopened, err := h.request(http.MethodPost, "/cases/"+thirdID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "fixture replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	if err := h.measureAccepted(secondID); err != nil {
		return err
	}
	if err := h.measureAccepted(thirdID); err != nil {
		return err
	}
	// looseID is intentionally left out of the commission but still active.
	_ = looseID
	for _, caseID := range []string{firstID, secondID, thirdID} {
		if err := h.sealCase(caseID); err != nil {
			return err
		}
	}

	closed, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/close", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(closed, http.StatusOK); err != nil {
		return err
	}
	closedComplete, err := nestedBool(closed.Body, "complete")
	if err != nil {
		return err
	}
	if !closedComplete {
		return errors.New("closed work order does not read complete")
	}
	if err := expectHasEvent(closed.Body, "work_order_closed"); err != nil {
		return err
	}

	// Once closed, new members can no longer be admitted.
	after, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/cases", map[string]any{
		"case_id": looseID,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(after, http.StatusConflict); err != nil {
		return err
	}
	finalView, err := h.getWorkOrder(workOrderID)
	if err != nil {
		return err
	}
	finalMembers, err := nestedSlicePath(finalView.Body, "work_order", "case_ids")
	if err != nil {
		return err
	}
	if len(finalMembers) != 3 {
		return fmt.Errorf("post-close attachment changed membership: %d members", len(finalMembers))
	}
	lateEvents, err := countEvents(finalView.Body, "case_attached", looseID)
	if err != nil {
		return err
	}
	if lateEvents != 0 {
		return fmt.Errorf("post-close rejection left %d acceptance events", lateEvents)
	}
	looseView, err := h.request(http.MethodGet, "/cases/"+looseID, nil)
	if err != nil {
		return err
	}
	if boundTo, _ := nestedString(looseView.Body, "case", "work_order_id"); boundTo != "" {
		return fmt.Errorf("loose case became bound to %q after close rejection", boundTo)
	}

	// Closing twice is a conflict, and ordinary sealed-case reads still work.
	again, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/close", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(again, http.StatusConflict); err != nil {
		return err
	}
	sealedCase, err := h.request(http.MethodGet, "/cases/"+firstID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(sealedCase, http.StatusOK); err != nil {
		return err
	}
	return nil
}

// checkWorkOrderRace hammers the same commission with concurrent attributions
// and closes. Whichever order the repository serializes them in, the result
// must be one consistent state, never a closed order carrying a late member or
// a case bound to two orders.
func checkWorkOrderRace() error {
	for iteration := 0; iteration < 20; iteration++ {
		h := newHarness()

		workOrderID, err := h.createWorkOrder("Race Lab", "Lin")
		if err != nil {
			h.close()
			return err
		}
		// One member is already sealed, so the order is closeable the moment
		// the racing attach loses.
		sealedID, err := h.openSealedCase(fmt.Sprintf("MET-16%02d", iteration))
		if err != nil {
			h.close()
			return err
		}
		if err := h.attachCase(workOrderID, sealedID); err != nil {
			h.close()
			return err
		}
		looseID, err := h.openAwaitingCase(fmt.Sprintf("MET-17%02d", iteration))
		if err != nil {
			h.close()
			return err
		}

		results := make(chan raceOutcome, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/cases", map[string]any{
				"case_id": looseID,
			})
			results <- raceOutcome{kind: "attach", response: response, err: err}
		}()
		go func() {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/close", nil)
			results <- raceOutcome{kind: "close", response: response, err: err}
		}()
		wait.Wait()
		close(results)

		var attachStatus, closeStatus int
		for item := range results {
			if item.err != nil {
				h.close()
				return item.err
			}
			switch item.kind {
			case "attach":
				if item.response.Status != http.StatusCreated && item.response.Status != http.StatusConflict {
					h.close()
					return fmt.Errorf("iteration %d: unexpected racing attach status %d: %s", iteration, item.response.Status, item.response.Raw)
				}
				attachStatus = item.response.Status
			case "close":
				if item.response.Status != http.StatusOK && item.response.Status != http.StatusConflict {
					h.close()
					return fmt.Errorf("iteration %d: unexpected racing close status %d: %s", iteration, item.response.Status, item.response.Raw)
				}
				closeStatus = item.response.Status
			}
		}

		finalView, err := h.getWorkOrder(workOrderID)
		if err != nil {
			h.close()
			return err
		}
		finalState, err := nestedString(finalView.Body, "work_order", "state")
		if err != nil {
			h.close()
			return err
		}
		finalMembers, err := nestedSlicePath(finalView.Body, "work_order", "case_ids")
		if err != nil {
			h.close()
			return err
		}
		finalComplete, err := nestedBool(finalView.Body, "complete")
		if err != nil {
			h.close()
			return err
		}
		looseView, err := h.request(http.MethodGet, "/cases/"+looseID, nil)
		if err != nil {
			h.close()
			return err
		}
		boundTo, _ := nestedString(looseView.Body, "case", "work_order_id")

		switch finalState {
		case "closed":
			// Close won: it must have seen the order sealable without the loose
			// case, and the loose case must stay unbound outside a closed order.
			if attachStatus == http.StatusCreated {
				h.close()
				return fmt.Errorf("iteration %d: closed order admitted racing member", iteration)
			}
			if closeStatus != http.StatusOK {
				h.close()
				return fmt.Errorf("iteration %d: order closed without a successful close response", iteration)
			}
			if len(finalMembers) != 1 {
				h.close()
				return fmt.Errorf("iteration %d: closed order carries %d members", iteration, len(finalMembers))
			}
			if !finalComplete {
				h.close()
				return fmt.Errorf("iteration %d: closed order does not read complete", iteration)
			}
			if boundTo != "" {
				h.close()
				return fmt.Errorf("iteration %d: loose case bound to %q after close won", iteration, boundTo)
			}
		case "open":
			// Attach won: the new member is unsealed, so the order cannot close.
			if attachStatus != http.StatusCreated {
				h.close()
				return fmt.Errorf("iteration %d: open order without a successful attach response", iteration)
			}
			if closeStatus == http.StatusOK {
				h.close()
				return fmt.Errorf("iteration %d: close reported success but order is open", iteration)
			}
			if len(finalMembers) != 2 || finalComplete {
				h.close()
				return fmt.Errorf("iteration %d: open order members=%d complete=%v", iteration, len(finalMembers), finalComplete)
			}
			if boundTo != workOrderID {
				h.close()
				return fmt.Errorf("iteration %d: attached case bound to %q, want %q", iteration, boundTo, workOrderID)
			}
			// The late member can still be worked and sealed normally, and once
			// it is, the order must be closeable.
			if err := h.measureAccepted(looseID); err != nil {
				h.close()
				return err
			}
			if err := h.sealCase(looseID); err != nil {
				h.close()
				return err
			}
			then, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/close", nil)
			if err != nil {
				h.close()
				return err
			}
			if err := expectStatus(then, http.StatusOK); err != nil {
				h.close()
				return fmt.Errorf("iteration %d: %w", iteration, err)
			}
		default:
			h.close()
			return fmt.Errorf("iteration %d: unexpected final work order state %q", iteration, finalState)
		}
		h.close()
	}

	// Second race shape: the same free case attributed concurrently to two
	// commissions. Exactly one may win; the case can end up in only one place.
	for iteration := 0; iteration < 20; iteration++ {
		h := newHarness()
		leftID, err := h.createWorkOrder("Left Lab", "Lin")
		if err != nil {
			h.close()
			return err
		}
		rightID, err := h.createWorkOrder("Right Lab", "Mira")
		if err != nil {
			h.close()
			return err
		}
		caseID, err := h.openAwaitingCase(fmt.Sprintf("MET-18%02d", iteration))
		if err != nil {
			h.close()
			return err
		}

		doubleResults := make(chan raceOutcome, 2)
		var doubleWait sync.WaitGroup
		doubleWait.Add(2)
		go func() {
			defer doubleWait.Done()
			response, err := h.request(http.MethodPost, "/workorders/"+leftID+"/cases", map[string]any{
				"case_id": caseID,
			})
			doubleResults <- raceOutcome{kind: "left", response: response, err: err}
		}()
		go func() {
			defer doubleWait.Done()
			response, err := h.request(http.MethodPost, "/workorders/"+rightID+"/cases", map[string]any{
				"case_id": caseID,
			})
			doubleResults <- raceOutcome{kind: "right", response: response, err: err}
		}()
		doubleWait.Wait()
		close(doubleResults)

		successes, conflicts := 0, 0
		for item := range doubleResults {
			if item.err != nil {
				h.close()
				return item.err
			}
			switch item.response.Status {
			case http.StatusCreated:
				successes++
			case http.StatusConflict:
				conflicts++
			default:
				h.close()
				return fmt.Errorf("double-attach iteration %d: unexpected status %d: %s", iteration, item.response.Status, item.response.Raw)
			}
		}
		if successes != 1 || conflicts != 1 {
			h.close()
			return fmt.Errorf("double-attach iteration %d: %d success, %d conflict", iteration, successes, conflicts)
		}
		caseView, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
		if err != nil {
			h.close()
			return err
		}
		boundTo, err := nestedString(caseView.Body, "case", "work_order_id")
		if err != nil {
			h.close()
			return err
		}
		if boundTo != leftID && boundTo != rightID {
			h.close()
			return fmt.Errorf("double-attach iteration %d: case bound to %q", iteration, boundTo)
		}
		loser := leftID
		if boundTo == leftID {
			loser = rightID
		}
		loserView, err := h.getWorkOrder(loser)
		if err != nil {
			h.close()
			return err
		}
		loserMembers, err := nestedSlicePath(loserView.Body, "work_order", "case_ids")
		if err != nil {
			h.close()
			return err
		}
		if len(loserMembers) != 0 {
			h.close()
			return fmt.Errorf("double-attach iteration %d: losing order still lists the case", iteration)
		}
		h.close()
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) createWorkOrder(customer, steward string) (string, error) {
	response, err := h.request(http.MethodPost, "/workorders", map[string]any{
		"customer": customer,
		"steward":  steward,
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "work_order", "id")
}

func (h *harness) getWorkOrder(id string) (apiResponse, error) {
	return h.request(http.MethodGet, "/workorders/"+id, nil)
}

func (h *harness) attachCase(workOrderID, caseID string) error {
	response, err := h.request(http.MethodPost, "/workorders/"+workOrderID+"/cases", map[string]any{
		"case_id": caseID,
	})
	if err != nil {
		return err
	}
	return expectStatus(response, http.StatusCreated)
}

func (h *harness) openAwaitingCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	return nestedString(opened.Body, "case", "id")
}

// openReadyCase opens a case in review_ready with an accepted measurement.
func (h *harness) openReadyCase(tag string) (string, error) {
	return h.readyCase(tag)
}

func (h *harness) openRecheckCase(tag string) (string, error) {
	return h.rejectedCase(tag)
}

func (h *harness) measureAccepted(caseID string) error {
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(response.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("case %s state after accepted measurement = %q", caseID, state)
	}
	return nil
}

func (h *harness) sealCase(caseID string) error {
	response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Sora",
		"summary":  "accepted measurement sealed for commission",
	})
	if err != nil {
		return err
	}
	return expectStatus(response, http.StatusCreated)
}

func (h *harness) openSealedCase(tag string) (string, error) {
	caseID, err := h.openReadyCase(tag)
	if err != nil {
		return "", err
	}
	if err := h.sealCase(caseID); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectHasEvent(root map[string]any, kind string) error {
	if hasEvent(root, kind) {
		return nil
	}
	return fmt.Errorf("expected %s event is missing", kind)
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func nestedSlicePath(root map[string]any, path ...any) ([]any, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%v is not an array", path)
	}
	return items, nil
}

// countEvents returns the number of work-order events matching the kind and,
// when caseID is non-empty, the referenced case.
func countEvents(root map[string]any, kind, caseID string) (int, error) {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] != kind {
			continue
		}
		if caseID != "" && event["case_id"] != caseID {
			continue
		}
		count++
	}
	return count, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
