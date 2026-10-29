// Package workstate serializes and atomically stores the workflow work model.
// It does not choose progression or infer missing execution or Git evidence.
package workstate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"gopkg.in/yaml.v3"
)

const formatVersion = 1

// Encode serializes a validated snapshot without advancing its persistence
// version. Save, not this codec, owns atomic expected-version storage.
func Encode(snapshot work.State) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workflow state: %w", err)
	}
	return yaml.Marshal(document{State: snapshot})
}

// Decode reads exactly one new-format document and validates its domain tree.
// It never interprets or converts old task records.
func Decode(data []byte) (work.State, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var decoded document
	if err := decoder.Decode(&decoded); err != nil {
		return work.State{}, fmt.Errorf("decode workflow state: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return work.State{}, fmt.Errorf("decode trailing workflow state: %w", err)
		}
		return work.State{}, fmt.Errorf("workflow state requires exactly one document")
	}
	if err := decoded.State.Validate(); err != nil {
		return work.State{}, fmt.Errorf("invalid workflow state: %w", err)
	}
	return decoded.State, nil
}

// document keeps serialization versioning separate from State.Version.
// The codec walks domain values directly: it has no second ownership tree.
type document struct{ State work.State }

func (d document) MarshalYAML() (any, error) {
	state, err := encodeValue(reflect.ValueOf(d.State))
	if err != nil {
		return nil, err
	}
	var version yaml.Node
	if err := version.Encode(formatVersion); err != nil {
		return nil, err
	}
	return mapping("format_version", &version, "state", state), nil
}

func (d *document) UnmarshalYAML(node *yaml.Node) error {
	fields, err := fieldsOf(node)
	if err != nil {
		return err
	}
	if len(fields) != 2 || fields["format_version"] == nil || fields["state"] == nil {
		return fmt.Errorf("workflow state requires only format_version and state; archive old task-state files outside the task-state directory before starting fresh")
	}
	var version int
	if err := fields["format_version"].Decode(&version); err != nil {
		return err
	}
	if version != formatVersion {
		return fmt.Errorf("unsupported workflow serialization format %d", version)
	}
	var decoded work.State
	if err := decodeValue(fields["state"], reflect.ValueOf(&decoded).Elem()); err != nil {
		return err
	}
	if err := decoded.Validate(); err != nil {
		return fmt.Errorf("invalid workflow state: %w", err)
	}
	d.State = decoded
	return nil
}

// alternatives is a closed wire vocabulary, not a generated-identity registry.
// A kind is admitted only when its concrete type implements the target interface.
var alternatives = map[string]reflect.Type{
	"preparation":           reflect.TypeFor[*work.PreparationPhase](),
	"implementation":        reflect.TypeFor[*work.ImplementationPhase](),
	"review":                reflect.TypeFor[*work.ReviewPhase](),
	"publication":           reflect.TypeFor[*work.PublicationPhase](),
	"external_review":       reflect.TypeFor[*work.ExternalReviewPhase](),
	"finalization":          reflect.TypeFor[*work.FinalizationPhase](),
	"check":                 reflect.TypeFor[*work.CheckStep](),
	"agent_review":          reflect.TypeFor[*work.AgentReviewStep](),
	"manual_review":         reflect.TypeFor[*work.ManualReviewStep](),
	"approved":              reflect.TypeFor[*work.ApprovedReview](),
	"requested_changes":     reflect.TypeFor[*work.RequestedChanges](),
	"aborted":               reflect.TypeFor[*work.AbortedReview](),
	"running":               reflect.TypeFor[*work.RunningProcess](),
	"exited":                reflect.TypeFor[*work.ExitedProcess](),
	"interrupted":           reflect.TypeFor[*work.InterruptedProcess](),
	"start_failed":          reflect.TypeFor[*work.StartFailedProcess](),
	"fresh":                 reflect.TypeFor[*work.FreshExecution](),
	"resumed":               reflect.TypeFor[*work.ResumedExecution](),
	"code":                  reflect.TypeFor[*work.CodeOutput](),
	"reused":                reflect.TypeFor[*work.ReusedOutput](),
	"repair_required":       reflect.TypeFor[*work.RepairRequired](),
	"promoted":              reflect.TypeFor[*work.PromotedFinding](),
	"waived":                reflect.TypeFor[*work.WaivedFinding](),
	"dismissed":             reflect.TypeFor[*work.DismissedFinding](),
	"downgraded":            reflect.TypeFor[*work.DowngradedFinding](),
	"addressed":             reflect.TypeFor[*work.AddressedFinding](),
	"pull_request_target":   reflect.TypeFor[*work.PullRequestTarget](),
	"direct_merge_target":   reflect.TypeFor[*work.DirectMergeTarget](),
	"pull_request_delivery": reflect.TypeFor[*work.PullRequestDelivery](),
	"direct_delivery":       reflect.TypeFor[*work.DirectDelivery](),
	"pr_acceptance":         reflect.TypeFor[*work.PRAcceptance](),
	"direct_acceptance":     reflect.TypeFor[*work.DirectAcceptance](),
	"worktree_removed":      reflect.TypeFor[*work.WorktreeRemoved](),
	"worktree_retained":     reflect.TypeFor[*work.WorktreeRetained](),
}

var requiredFields = map[reflect.Type][]string{
	reflect.TypeFor[work.State]():            {"Version"},
	reflect.TypeFor[work.OutputRef]():        {"Phase"},
	reflect.TypeFor[work.ReviewRef]():        {"Phase"},
	reflect.TypeFor[work.PublicationRef]():   {"Phase"},
	reflect.TypeFor[work.ExecutionRef]():     {"Phase"},
	reflect.TypeFor[work.FindingRef]():       {"Phase", "Step", "Finding"},
	reflect.TypeFor[work.AttemptRef]():       {"Workflow", "Phase"},
	reflect.TypeFor[work.ReviewPhase]():      {"Contract"},
	reflect.TypeFor[work.CheckStep]():        {"Slot"},
	reflect.TypeFor[work.AgentReviewStep]():  {"Slot"},
	reflect.TypeFor[work.ManualReviewStep](): {"Slot"},
	reflect.TypeFor[work.PRAcceptance]():     {"ExternalReview"},
}

var (
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	wordBoundary    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

func fieldName(name string) string {
	return strings.ToLower(wordBoundary.ReplaceAllString(acronymBoundary.ReplaceAllString(name, "${1}_${2}"), "${1}_${2}"))
}

func mapping(key string, value *yaml.Node, rest ...any) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	addField(node, key, value)
	for i := 0; i < len(rest); i += 2 {
		addField(node, rest[i].(string), rest[i+1].(*yaml.Node))
	}
	return node
}

func addField(node *yaml.Node, key string, value *yaml.Node) {
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func encodeValue(value reflect.Value) (*yaml.Node, error) {
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		return encodeAlternative(value.Elem())
	case reflect.Pointer:
		return encodeValue(value.Elem())
	case reflect.Struct:
		if value.Type() != reflect.TypeFor[time.Time]() {
			return encodeStruct(value)
		}
	case reflect.Slice:
		if value.IsNil() {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}, nil
		}
		return encodeSlice(value)
	}
	var node yaml.Node
	if err := node.Encode(value.Interface()); err != nil {
		return nil, err
	}
	// yaml.v3 can emit invalid indentation for leading-space multiline values
	// nested in sequences. Quote those scalars explicitly instead of rejecting
	// legitimate agent prompts or storing bytes that cannot be independently read.
	if value.Kind() == reflect.String && strings.ContainsAny(value.String(), "\r\n") {
		node.Style = yaml.DoubleQuotedStyle
	}
	return &node, nil
}

func encodeAlternative(value reflect.Value) (*yaml.Node, error) {
	for kind, alternative := range alternatives {
		if value.Type() != alternative {
			continue
		}
		body, err := encodeValue(value)
		if err != nil {
			return nil, err
		}
		return mapping("kind", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kind}, "value", body), nil
	}
	return nil, fmt.Errorf("unsupported workflow alternative %s", value.Type())
}

func encodeStruct(value reflect.Value) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		child, err := encodeValue(value.Field(i))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.Name, err)
		}
		addField(node, fieldName(field.Name), child)
	}
	return node, nil
}

func encodeSlice(value reflect.Value) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for i := 0; i < value.Len(); i++ {
		child, err := encodeValue(value.Index(i))
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		node.Content = append(node.Content, child)
	}
	return node, nil
}

func fieldsOf(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("expected an explicit mapping")
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || fields[key.Value] != nil {
			return nil, fmt.Errorf("invalid or duplicate workflow field %q", key.Value)
		}
		fields[key.Value] = node.Content[i+1]
	}
	return fields, nil
}

func decodeValue(node *yaml.Node, into reflect.Value) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("workflow aliases are not supported")
	}
	if node.Tag == "!!null" {
		if into.Kind() != reflect.Interface && into.Kind() != reflect.Pointer && into.Kind() != reflect.Slice {
			return fmt.Errorf("null cannot represent %s", into.Type())
		}
		into.SetZero()
		return nil
	}
	switch into.Kind() {
	case reflect.Interface:
		return decodeAlternative(node, into)
	case reflect.Pointer:
		into.Set(reflect.New(into.Type().Elem()))
		return decodeValue(node, into.Elem())
	case reflect.Struct:
		if into.Type() != reflect.TypeFor[time.Time]() {
			return decodeStruct(node, into)
		}
	case reflect.Slice:
		return decodeSlice(node, into)
	}
	return node.Decode(into.Addr().Interface())
}

func decodeAlternative(node *yaml.Node, into reflect.Value) error {
	fields, err := fieldsOf(node)
	if err != nil {
		return err
	}
	if len(fields) != 2 || fields["kind"] == nil || fields["value"] == nil {
		return fmt.Errorf("closed workflow alternatives require only kind and value")
	}
	kind := fields["kind"]
	typeOf, ok := alternatives[kind.Value]
	if !ok || kind.Tag != "!!str" || !typeOf.Implements(into.Type()) {
		return fmt.Errorf("unsupported %s kind %q", into.Type(), kind.Value)
	}
	if fields["value"].Tag == "!!null" {
		return fmt.Errorf("workflow alternative %q requires a value", kind.Value)
	}
	value := reflect.New(typeOf.Elem())
	if err := decodeValue(fields["value"], value.Elem()); err != nil {
		return fmt.Errorf("%s: %w", kind.Value, err)
	}
	into.Set(value)
	return nil
}

func decodeStruct(node *yaml.Node, into reflect.Value) error {
	fields, err := fieldsOf(node)
	if err != nil {
		return err
	}
	for _, field := range requiredFields[into.Type()] {
		value := fields[fieldName(field)]
		if value == nil || value.Tag == "!!null" || value.Tag != "!!int" {
			return fmt.Errorf("%s.%s requires an explicit integer", into.Type(), field)
		}
	}
	for i := 0; i < into.NumField(); i++ {
		field := into.Type().Field(i)
		name := fieldName(field.Name)
		if fields[name] == nil && field.Type.Kind() == reflect.Struct && containsRequiredAddress(field.Type) {
			return fmt.Errorf("%s.%s requires an explicit address", into.Type(), field.Name)
		}
		if child := fields[name]; child != nil {
			if err := decodeValue(child, into.Field(i)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(fields, name)
		}
	}
	if len(fields) != 0 {
		return fmt.Errorf("unknown %s fields: %v", into.Type(), reflect.ValueOf(fields).MapKeys())
	}
	return nil
}

func containsRequiredAddress(typeOf reflect.Type) bool {
	if len(requiredFields[typeOf]) != 0 {
		return true
	}
	if typeOf.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		if field.Type.Kind() == reflect.Struct && containsRequiredAddress(field.Type) {
			return true
		}
	}
	return false
}

func decodeSlice(node *yaml.Node, into reflect.Value) error {
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("expected a sequence for %s", into.Type())
	}
	into.Set(reflect.MakeSlice(into.Type(), len(node.Content), len(node.Content)))
	for i, child := range node.Content {
		if err := decodeValue(child, into.Index(i)); err != nil {
			return fmt.Errorf("element %d: %w", i, err)
		}
	}
	return nil
}
