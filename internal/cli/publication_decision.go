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

// Publication prompting records decisions only. Delivery always requires a
// subsequent advancement, even if rendering the recorded answer fails.
func presentPublicationDecision(command *cobra.Command, controller workflow.Controller, reader *bufio.Reader, view workflow.View, terminal TerminalCapabilities) error {
	terminal = terminal.withDefaults()
	interactive := terminal.InputIsTerminal(command.InOrStdin()) && terminal.OutputIsTerminal(command.OutOrStdout()) && terminal.OutputIsTerminal(command.ErrOrStderr())
	for view.Pending != nil && view.Pending.ID.Kind == workflow.InteractionPublication {
		if err := controller.Progress(workflow.Progress{View: view}); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(command.ErrOrStderr(), "PUBLICATION CONTENT\n"+view.Publication.Content); err != nil {
			return err
		}
		if !interactive {
			return renderWorkflowNext(command.OutOrStdout(), view)
		}
		response, err := promptPublicationResponse(command, reader, view)
		if err != nil {
			return err
		}
		if _, paused := response.(workflow.PauseResponse); paused {
			return nil
		}
		view, err = controller.Respond(command.Context(), view.Pending.ID, response)
		if err != nil {
			return err
		}
	}
	if view.Publication != nil && view.Publication.Consented {
		_, err := fmt.Fprintln(command.OutOrStdout(), "Publication consent recorded for this plan. Run task run to continue delivery.")
		return err
	}
	return renderWorkflowNext(command.OutOrStdout(), view)
}

func promptPublicationResponse(command *cobra.Command, reader *bufio.Reader, view workflow.View) (workflow.Response, error) {
	for {
		if _, err := fmt.Fprint(command.ErrOrStderr(), "Publication decision [a=authorize this plan, i=flow, d=destination, q=pause]: "); err != nil {
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
		case "a", "authorize":
			return workflow.PublicationResponse{Action: workflow.DecisionAuthorize, Evidence: "Operator authorized the displayed publication plan"}, nil
		case "", "q", "pause":
			return workflow.PauseResponse{}, nil
		case "i", "flow", "d", "destination":
			return promptPublicationPlanChange(command, reader, view, action == "i" || action == "flow")
		}
	}
}

func promptPublicationPlanChange(command *cobra.Command, reader *bufio.Reader, view workflow.View, changeFlow bool) (workflow.Response, error) {
	proposal := workflow.PublicationPlanResponse{Flow: publication.IntegrationFlow(view.Publication.Flow), DestinationBranch: view.Publication.Destination.Branch, Content: view.Publication.Content}
	prompt := "Destination branch (requires fresh review when changed): "
	if changeFlow {
		prompt = "Flow [pull-request/direct-merge]: "
	}
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
	if changeFlow {
		proposal.Flow = publication.IntegrationFlow(strings.TrimSpace(value))
	} else {
		proposal.DestinationBranch = strings.TrimSpace(value)
	}
	return proposal, nil
}
