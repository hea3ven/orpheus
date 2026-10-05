package gig

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataRoundTripPreservesSupplementalFieldsAndUnrelatedValues(t *testing.T) {
	now := time.Now().UTC()
	original := task.Task{ID: "op-1", Design: "design", AcceptanceCriteria: "acceptance", ExternalRef: "REF-1", Owner: "operator", StartedAt: &now, CompletedAt: &now,
		Metadata: task.Metadata{task.MetadataBranch: "branch", task.MetadataWorktree: "/worktree", task.MetadataPRURL: "url"}}
	stored := storedTask{item: original, metadata: map[string]json.RawMessage{"custom": json.RawMessage(`{"nested":true}`), fieldsKey: json.RawMessage(`{"future":"keep"}`)}}
	raw, err := encodeMetadata(stored)
	require.NoError(t, err)
	decoded, err := decodeMetadata(task.Task{ID: original.ID}, raw)
	require.NoError(t, err)
	assert.Equal(t, original.Design, decoded.item.Design)
	assert.Equal(t, original.AcceptanceCriteria, decoded.item.AcceptanceCriteria)
	assert.Equal(t, original.ExternalRef, decoded.item.ExternalRef)
	assert.Equal(t, original.Owner, decoded.item.Owner)
	assert.Equal(t, original.StartedAt, decoded.item.StartedAt)
	assert.Equal(t, original.CompletedAt, decoded.item.CompletedAt)
	assert.Equal(t, original.OrpheusMetadata(), decoded.item.OrpheusMetadata())
	assert.JSONEq(t, `{"nested":true}`, decoded.item.Metadata["custom"])
	assert.NotContains(t, decoded.item.Metadata, fieldsKey)
	assert.Contains(t, string(decoded.metadata[fieldsKey]), `"future":"keep"`)
}

func TestMetadataRejectsMalformedSourceData(t *testing.T) {
	for _, raw := range []string{`not-json`, `[]`, `{"orpheus.gig":"invalid"}`, `{"orpheus.gig":{"started_at":"yesterday"}}`} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeMetadata(task.Task{ID: "op-1"}, raw)
			require.Error(t, err)
		})
	}
	for _, raw := range []string{"", "null", "{}"} {
		decoded, err := decodeMetadata(task.Task{ID: "op-1"}, raw)
		require.NoError(t, err)
		assert.NotNil(t, decoded.metadata)
		assert.NotNil(t, decoded.item.Metadata)
	}
}

func TestBackendRequiresManagedStorageAndPrefix(t *testing.T) {
	for _, source := range []task.RepositorySource{
		{},
		{MaintenanceOwned: true, BackendDir: "relative", Repository: task.Repository{TaskIDPrefix: "op"}},
		{MaintenanceOwned: true, BackendDir: "/fixture/gig"},
		{MaintenanceOwned: true, BackendDir: "/fixture/percent%41", Repository: task.Repository{TaskIDPrefix: "op"}},
		{MaintenanceOwned: true, BackendDir: "/fixture/question?", Repository: task.Repository{TaskIDPrefix: "op"}},
		{MaintenanceOwned: true, BackendDir: "/fixture/fragment#", Repository: task.Repository{TaskIDPrefix: "op"}},
	} {
		_, err := New(source)
		require.Error(t, err)
	}
}
