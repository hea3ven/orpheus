package gig

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
)

const fieldsKey = "orpheus.gig"

// supplementalFields contains fields gig does not natively represent. It lives
// in the source metadata, never in Orpheus execution/review history.
type supplementalFields struct {
	Design             string     `json:"design,omitempty"`
	AcceptanceCriteria string     `json:"acceptance_criteria,omitempty"`
	ExternalRef        string     `json:"external_ref,omitempty"`
	Owner              string     `json:"owner,omitempty"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type storedTask struct {
	item     task.Task
	metadata map[string]json.RawMessage
}

func decodeMetadata(item task.Task, raw string) (storedTask, error) {
	values := make(map[string]json.RawMessage)
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return storedTask{}, fmt.Errorf("task %s metadata: %w", item.ID, err)
		}
	}
	if values == nil {
		values = make(map[string]json.RawMessage)
	}
	var fields supplementalFields
	if value, ok := values[fieldsKey]; ok {
		if err := json.Unmarshal(value, &fields); err != nil {
			return storedTask{}, fmt.Errorf("task %s adapter metadata: %w", item.ID, err)
		}
	}
	item.Design, item.AcceptanceCriteria, item.ExternalRef = fields.Design, fields.AcceptanceCriteria, fields.ExternalRef
	item.Owner, item.StartedAt, item.CompletedAt = fields.Owner, fields.StartedAt, fields.CompletedAt
	item.Metadata = make(task.Metadata)
	for key, value := range values {
		if key == fieldsKey {
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			text = string(value)
		}
		item.Metadata[key] = text
	}
	return storedTask{item: item, metadata: values}, nil
}

func encodeMetadata(stored storedTask) (string, error) {
	// Merge only known fields. Unknown source metadata, including future fields
	// inside our namespace, survives content and lifecycle mutations unchanged.
	fields := make(map[string]json.RawMessage)
	if raw, ok := stored.metadata[fieldsKey]; ok {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", err
		}
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	item := stored.item
	for key, value := range map[string]any{"design": item.Design, "acceptance_criteria": item.AcceptanceCriteria,
		"external_ref": item.ExternalRef, "owner": item.Owner, "started_at": item.StartedAt, "completed_at": item.CompletedAt} {
		raw, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		fields[key] = raw
	}
	rawFields, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	stored.metadata[fieldsKey] = rawFields
	for _, key := range []string{task.MetadataBranch, task.MetadataWorktree, task.MetadataPRURL} {
		if value, ok := item.Metadata[key]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			stored.metadata[key] = raw
		}
	}
	raw, err := json.Marshal(stored.metadata)
	return string(raw), err
}
