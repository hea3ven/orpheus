package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

// Publication prompting records decisions through the typed boundary. The caller
// advances only after a successful answer, never on pause or unavailable input.
func presentPublicationDecision(command *cobra.Command, controller workflow.Controller, reader *bufio.Reader, view workflow.View, terminal TerminalCapabilities) (workflow.View, error) {
	for view.Pending != nil && (view.Pending.ID.Kind == workflow.InteractionPublication || view.Pending.ID.Kind == workflow.InteractionPublicationRecovery) {
		if err := controller.Progress(workflow.Progress{View: view}); err != nil {
			return view, err
		}
		if view.Pending.ID.Kind == workflow.InteractionPublicationRecovery {
			next, err := presentPublicationRecovery(command, controller, reader, view, terminal)
			return next, err
		}
		if _, err := fmt.Fprintln(command.ErrOrStderr(), "PUBLICATION CONTENT\n"+view.Publication.Content); err != nil {
			return view, err
		}
		if view.Publication.Input.Provenance == "operator_selected" {
			if _, err := fmt.Fprintf(command.ErrOrStderr(), "NOTICE: publication input differs from reviewed output; it has not passed that review.\nReviewed commit: %s\nActual input: %s (tree %s)\nRelationship: %s\nChanges from review:\n%s\nProceed explicitly accepts this review gap; abort leaves publication pending.\n", view.Revision.Code.Commit, view.Publication.Input.Code.Commit, view.Publication.Input.Code.Tree, view.Publication.Comparison.Relationship, view.Publication.Comparison.Changes); err != nil {
				return view, err
			}
		}
		if err := requireOperatorInput(command, terminal, view.TaskID); err != nil {
			return view, err
		}
		response, err := promptPublicationResponse(command, reader, view)
		if err != nil {
			return view, err
		}
		if _, paused := response.(workflow.PauseResponse); paused {
			return view, nil
		}
		view, err = controller.Respond(command.Context(), view.Pending.ID, response)
		if err != nil {
			return view, err
		}
	}
	return view, nil
}

func promptPublicationResponse(command *cobra.Command, reader *bufio.Reader, view workflow.View) (workflow.Response, error) {
	for {
		if _, err := fmt.Fprint(command.ErrOrStderr(), "Publication decision [p=proceed with this exact input and plan, b=abort, i=flow, q=pause]: "); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return workflow.PauseResponse{}, nil
		}
		if err != nil {
			return nil, err
		}
		action := strings.ToLower(strings.TrimSpace(line))
		switch action {
		case "p", "proceed", "a", "authorize":
			return workflow.PublicationResponse{Action: workflow.DecisionAuthorize, Evidence: "Operator chose proceed for the displayed exact publication input and plan, including any disclosed review gap"}, nil
		case "", "b", "abort", "q", "pause":
			return workflow.PauseResponse{}, nil
		case "i", "flow":
			return promptPublicationPlanChange(command, reader, view)
		}
	}
}

func promptPublicationPlanChange(command *cobra.Command, reader *bufio.Reader, view workflow.View) (workflow.Response, error) {
	proposal := workflow.PublicationPlanResponse{Flow: publication.IntegrationFlow(view.Publication.Flow), Content: view.Publication.Content}
	prompt := "Flow [pull-request/direct-merge]: "
	if _, err := fmt.Fprint(command.ErrOrStderr(), prompt); err != nil {
		return nil, err
	}
	value, err := reader.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return workflow.PauseResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	proposal.Flow = publication.IntegrationFlow(strings.TrimSpace(value))
	return proposal, nil
}

func presentPublicationRecovery(command *cobra.Command, controller workflow.Controller, reader *bufio.Reader, view workflow.View, terminal TerminalCapabilities) (workflow.View, error) {
	disclosure, err := controller.InspectPublicationRecovery(command.Context(), view.Pending.ID)
	if err != nil {
		return view, err
	}
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "PUBLICATION RECOVERY\nSaved input: %s (tree %s)\n%s\n", view.Publication.Input.Code.Commit, view.Publication.Input.Code.Tree, disclosure.Changes); err != nil {
		return view, err
	}
	if err := requireOperatorInput(command, terminal, view.TaskID); err != nil {
		return view, err
	}
	if _, err := fmt.Fprint(command.ErrOrStderr(), "Recovery [r=restore saved input, discard displayed changes and retry, m=leave untouched for manual repair, q=pause]: "); err != nil {
		return view, err
	}
	answer, err := reader.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	if strings.TrimSpace(strings.ToLower(answer)) != "r" {
		return view, nil
	}
	return controller.Respond(command.Context(), view.Pending.ID, workflow.PublicationRecoveryResponse{Checkout: disclosure})
}
