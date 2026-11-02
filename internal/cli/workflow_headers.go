package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

// workflowHeaders is invocation-scoped. It retains presentation state only;
// attempts and continuation always come from workflow facts.
type workflowHeaders struct {
	output      io.Writer
	terminal    TerminalCapabilities
	style       manualReviewStyle
	lastContext string
	lastStep    string
}

type workflowHeaderField struct{ label, value string }

func newWorkflowHeaders(command *cobra.Command, terminal TerminalCapabilities) *workflowHeaders {
	terminal = terminal.withDefaults()
	return &workflowHeaders{output: command.ErrOrStderr(), terminal: terminal, style: manualReviewStyle{enabled: !manualReviewColorIsDisabled() && terminal.OutputIsTerminal(command.OutOrStdout()) && terminal.OutputIsTerminal(command.ErrOrStderr())}}
}

func (h *workflowHeaders) Render(progress workflow.Progress) error {
	v := progress.View
	fields := workflowHeaderFields(v)
	phase := strings.ToUpper(strings.ReplaceAll(string(v.Phase), "_", " "))
	if v.PhaseAttempt > 1 {
		phase += fmt.Sprintf(" | attempt %d", v.PhaseAttempt)
	}
	contextKey := fmt.Sprintf("%v/%v/%s/%v", v.Workflow, v.Owner, phase, fields)
	// Compare containment values rather than pointer addresses in the key.
	if v.Owner != nil {
		contextKey = fmt.Sprintf("%d/%d/%s/%v", v.Owner.Workflow, v.Owner.Phase, phase, fields)
	}
	if contextKey != h.lastContext {
		if err := h.phase(phase, fields, progress.Resumed); err != nil {
			return err
		}
		h.lastContext = contextKey
		h.lastStep = ""
	}
	if v.Step == nil {
		return nil
	}
	key := fmt.Sprintf("%s/%s/%d", v.Step.Name, v.Step.Kind, v.Step.Attempt)
	if key == h.lastStep {
		return nil
	}
	heading := fmt.Sprintf("STEP: %s (%s)", v.Step.Name, v.Step.Kind)
	if v.Step.Attempt > 1 {
		heading += fmt.Sprintf(" | attempt %d", v.Step.Attempt)
	}
	if h.style.enabled {
		heading = "◇ " + strings.Replace(heading, "STEP:", "STEP ·", 1)
	} else {
		heading = "-- " + heading + " --"
	}
	for _, line := range wrapWorkflowValue(heading, h.width()) {
		if _, err := fmt.Fprintln(h.output, h.style.blue(line)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(h.output); err != nil {
		return err
	}
	h.lastStep = key
	return nil
}

func (h *workflowHeaders) phase(phase string, fields []workflowHeaderField, resumed bool) error {
	heading := "== " + phase + " =="
	if resumed {
		heading = "== " + phase + " | resumed =="
	}
	if h.style.enabled {
		heading = "◆ " + phase
		if resumed {
			heading += " · resumed"
		}
	}
	if _, err := fmt.Fprintln(h.output); err != nil {
		return err
	}
	for _, line := range wrapWorkflowValue(heading, h.width()) {
		if _, err := fmt.Fprintln(h.output, h.style.blue(h.style.bold(line))); err != nil {
			return err
		}
	}
	for _, field := range fields {
		if err := h.field(field); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(h.output)
	return err
}

func workflowHeaderFields(v workflow.View) []workflowHeaderField {
	fields := []workflowHeaderField{{"TASK", v.TaskID + " - " + v.Title}}
	add := func(label, value string) {
		if value != "" {
			fields = append(fields, workflowHeaderField{label, value})
		}
	}
	if v.WorkflowCount > 1 {
		add("WORKFLOW", fmt.Sprint(v.Workflow))
	}
	switch v.Phase {
	case workflow.PhasePreparation:
		if v.Context != nil {
			add("WORKDIR", v.Context.Directory)
			add("BRANCH", v.Context.TaskBranch)
			add("BASELINE", v.Context.Baseline.Commit)
		}
	case workflow.PhaseImplementation:
		if v.Execution != nil {
			add("AGENT", v.Execution.Profile+" (implementer)")
		}
		if v.Start != nil {
			add("START", workflowRevisionLabel(v.Start))
		} else if v.Context != nil {
			add("START", v.Context.Baseline.Commit)
		}
		if len(v.Findings) > 0 {
			labels := make([]string, 0, len(v.Findings))
			for _, ref := range v.Findings {
				labels = append(labels, fmt.Sprintf("phase %d / step %d / finding %d", ref.Phase+1, ref.Step+1, ref.Finding+1))
			}
			add("FINDINGS", strings.Join(labels, ", "))
		}
		add("REVISION", workflowRevisionLabel(v.Revision))
	case workflow.PhaseReview:
		add("REVISION", workflowRevisionLabel(v.Revision))
		if v.Scope != nil {
			add("SCOPE", "Full task | "+v.Scope.Baseline+" -> "+v.Scope.Head)
		}
		if v.Execution != nil {
			add("AGENT", v.Execution.Profile+" (reviewer)")
		}
		if v.Published != nil {
			add("PUBLISHED", workflowRevisionLabel(v.Published.Revision)+" | public commit "+v.Published.PublicCommit)
			add("PR", v.Published.PRURL)
		}
	case workflow.PhasePublication, workflow.PhaseExternalReview, workflow.PhaseFinalization:
		fields = append(fields, publicationHeaderFields(v)...)
	}
	return fields
}

func publicationHeaderFields(v workflow.View) []workflowHeaderField {
	p := v.Publication
	if p == nil {
		return nil
	}
	destination := p.Destination.Branch
	if p.Destination.Remote != "" {
		destination = p.Destination.Remote + "/" + destination
	}
	if p.Destination.Commit != "" {
		destination += " at " + p.Destination.Commit
	}
	switch v.Phase {
	case workflow.PhasePublication:
		consent := "Awaiting publication decision"
		if p.Consented {
			consent = "Recorded for this unchanged plan"
		}
		return []workflowHeaderField{{"REVISION", workflowRevisionLabel(v.Revision)}, {"REVIEW", fmt.Sprintf("Attempt %d %s | phase %d", p.ReviewAttempt, p.ReviewOutcome, p.Review.Phase)}, {"PLAN", fmt.Sprintf("phase %d | %s", p.Plan.Phase, p.Operation)}, {"FLOW", p.Flow}, {"DESTINATION", destination}, {"PR", p.PRURL}, {"EXPECTED HEAD", p.ExpectedHead}, {"EFFECTS", strings.Join(p.Effects, "; ")}, {"CONSENT", consent}}
	case workflow.PhaseExternalReview:
		return []workflowHeaderField{{"PR", p.PRURL + " | " + p.PRState}, {"PUBLISHED", workflowRevisionLabel(v.Revision) + " | public commit " + p.PublicCommit}, {"DESTINATION", destination}}
	case workflow.PhaseFinalization:
		evidence := p.DeliveryEvidence
		if p.AcceptanceEvidence != "" {
			evidence = p.AcceptanceEvidence
		}
		fields := []workflowHeaderField{{"REVISION", workflowRevisionLabel(v.Revision)}, {"DELIVERY", evidence}}
		if v.Closure != nil {
			fields = append(fields, workflowHeaderField{"CLOSURE", v.Closure.Evidence})
		}
		return fields
	}
	return nil
}

func workflowRevisionLabel(v *workflow.RevisionView) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("R%d | %s", v.Number, v.Code.Commit)
}

func (h *workflowHeaders) width() int { width, _ := h.terminal.OutputWidth(h.output); return width }

func (h *workflowHeaders) field(field workflowHeaderField) error {
	const indent = 13
	width := h.width()
	prefix := fmt.Sprintf("%-*s", indent, field.label)
	padding := strings.Repeat(" ", indent)
	valueWidth := width - indent
	if width > 0 && valueWidth < 8 {
		if _, err := fmt.Fprintln(h.output, h.style.blue(field.label)); err != nil {
			return err
		}
		prefix = "  "
		padding = prefix
		valueWidth = width - 2
	}
	if width <= 0 {
		valueWidth = 0
	}
	for i, line := range wrapWorkflowValue(field.value, valueWidth) {
		label := padding
		if i == 0 {
			label = h.style.blue(prefix)
		}
		if field.label == "TASK" {
			line = h.style.bold(line)
		}
		if field.label == "CONSENT" && strings.HasPrefix(field.value, "Awaiting") {
			line = h.style.amber(line)
		}
		if _, err := fmt.Fprintln(h.output, label+line); err != nil {
			return err
		}
	}
	return nil
}

// wrapWorkflowValue preserves complete identifiers, including long paths and
// wide/combining characters, without truncating at a byte or terminal cell.
func wrapWorkflowValue(value string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(value, "\n") {
		paragraph = expandTailTabs(sanitizeTailText(paragraph))
		if width <= 0 {
			lines = append(lines, paragraph)
			continue
		}
		var line strings.Builder
		cells := 0
		for _, r := range paragraph {
			size := runewidth.RuneWidth(r)
			if cells+size > width && line.Len() > 0 {
				lines = append(lines, line.String())
				line.Reset()
				cells = 0
			}
			line.WriteRune(r)
			cells += size
		}
		lines = append(lines, line.String())
	}
	return lines
}
