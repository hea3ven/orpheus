package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func newTaskShowWorkflowCommand(opts *rootOptions) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{Use: "workflow <task-id>", Short: "Inspect workflow context, next action, and pending typed decisions", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		controller, err := workflowControllerForCommand(command, opts)
		if err != nil {
			return err
		}
		view, err := controller.Inspect(command.Context(), args[0])
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(command.OutOrStdout()).Encode(view)
		}
		headers := newWorkflowHeaders(command, TerminalCapabilities{})
		headers.output = command.OutOrStdout()
		if err := headers.Render(workflow.Progress{View: view}); err != nil {
			return err
		}
		if view.Pending != nil && view.Publication != nil {
			if _, err := fmt.Fprintln(command.OutOrStdout(), view.Publication.Content); err != nil {
				return err
			}
		}
		return renderWorkflowNext(command.OutOrStdout(), view)
	}}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write semantic workflow facts and interaction identity as JSON")
	return cmd
}

func newTaskRespondCommand(opts *rootOptions) *cobra.Command {
	var idJSON, action, evidence, decisionsJSON, planJSON string
	cmd := &cobra.Command{Use: "respond <task-id>", Short: "Record a typed workflow decision without advancing or publishing", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		var id workflow.InteractionID
		if err := decodeWorkflowJSON(idJSON, &id); err != nil {
			return fmt.Errorf("decode --interaction: %w", err)
		}
		if id.Owner.TaskID != args[0] {
			return fmt.Errorf("interaction belongs to another task")
		}
		response, err := parseWorkflowResponse(id.Kind, workflow.DecisionAction(action), evidence, decisionsJSON, planJSON)
		if err != nil {
			return err
		}
		controller, err := workflowControllerForCommand(command, opts)
		if err != nil {
			return err
		}
		view, err := controller.Respond(command.Context(), id, response)
		if err != nil {
			return err
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(view)
	}}
	cmd.Flags().StringVar(&idJSON, "interaction", "", "exact interaction ID JSON from task show workflow --json")
	cmd.Flags().StringVar(&action, "action", "", "explicit permitted action; publication accepts authorize, revise, or pause")
	cmd.Flags().StringVar(&evidence, "evidence", "", "operator evidence for exact-plan publication consent")
	cmd.Flags().StringVar(&planJSON, "plan", "", "complete replacement plan JSON: Flow, DestinationBranch, Content (with --action revise)")
	cmd.Flags().StringVar(&decisionsJSON, "decisions", "", "JSON array of explicit finding dispositions")
	return cmd
}

func parseWorkflowResponse(kind workflow.InteractionKind, action workflow.DecisionAction, evidence, decisionsJSON, planJSON string) (workflow.Response, error) {
	if planJSON != "" {
		if kind != workflow.InteractionPublication || action != workflow.DecisionRevise || evidence != "" || decisionsJSON != "" {
			return nil, fmt.Errorf("--plan requires only a publication revise response")
		}
		var proposal workflow.PublicationPlanResponse
		if err := decodeWorkflowJSON(planJSON, &proposal); err != nil {
			return nil, fmt.Errorf("decode --plan: %w", err)
		}
		return proposal, nil
	}
	switch kind {
	case workflow.InteractionManualReview:
		if decisionsJSON != "" || evidence != "" {
			return nil, fmt.Errorf("manual assessment does not accept publication evidence or finding dispositions")
		}
		return workflow.ManualResponse{Action: action}, nil
	case workflow.InteractionPublication:
		if decisionsJSON != "" {
			return nil, fmt.Errorf("publication decision does not accept finding dispositions")
		}
		return workflow.PublicationResponse{Action: action, Evidence: evidence}, nil
	case workflow.InteractionFindings:
		if action == workflow.DecisionPause && evidence == "" && decisionsJSON == "" {
			return workflow.PauseResponse{}, nil
		}
		if action != "" || evidence != "" {
			return nil, fmt.Errorf("finding decisions require --decisions only")
		}
		var decisions []workflow.FindingDecision
		if err := decodeWorkflowJSON(decisionsJSON, &decisions); err != nil {
			return nil, err
		}
		return workflow.FindingsResponse{Decisions: decisions}, nil
	default:
		return nil, fmt.Errorf("unsupported interaction kind %q", kind)
	}
}

func decodeWorkflowJSON(input string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func workflowControllerForCommand(command *cobra.Command, opts *rootOptions) (workflow.Controller, error) {
	deps, err := opts.invocation(command)
	if err != nil {
		return workflow.Controller{}, err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return workflow.Controller{}, err
	}
	return newTaskWorkflowController(command, deps, taskCtx, opts.log(), bufio.NewReader(command.InOrStdin())), nil
}

func renderWorkflowNext(output io.Writer, view workflow.View) error {
	if _, err := fmt.Fprintf(output, "Workflow next action: %s\n", view.Next.Action); err != nil {
		return err
	}
	if view.Next.Advice != "" {
		if _, err := fmt.Fprintln(output, view.Next.Advice); err != nil {
			return err
		}
	}
	if view.Pending == nil {
		return nil
	}
	if _, err := fmt.Fprintf(output, "Pending decision: %s\nLegal responses: %v\n", view.Pending.ID.Kind, view.Pending.Actions); err != nil {
		return err
	}
	return nil
}
