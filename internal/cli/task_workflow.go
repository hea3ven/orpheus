package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hea3ven/orpheus/internal/work"
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
		if view.PublicationContent != nil {
			if _, err := fmt.Fprintln(command.OutOrStdout(), renderPublicationContent(view.PublicationContent)); err != nil {
				return err
			}
		}
		return renderWorkflowNext(command.OutOrStdout(), view)
	}}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write semantic workflow facts and interaction identity as JSON")
	return cmd
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
	if view.ResetPending {
		if _, err := fmt.Fprintln(output, "Restart pending: the next task run will discard checkout changes during Preparation."); err != nil {
			return err
		}
	}
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

func renderPublicationContent(c *work.PublicationContent) string {
	if c == nil {
		return "Publication content not initialized"
	}
	return "PUBLICATION COMMIT\n" + c.CommitMessage() + "\n\nPR DESCRIPTION\n" + c.PRDescription
}
