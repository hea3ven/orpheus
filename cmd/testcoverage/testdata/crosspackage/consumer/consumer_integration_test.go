//go:build integration

package consumer

import (
	"testing"

	"example.test/crosspackage/collaborator"
)

func TestIntegrationConsumerCreditsCollaborator(t *testing.T) {
	if got := collaborator.Collaborate(41); got != 42 {
		t.Fatalf("Collaborate() = %d, want 42", got)
	}
}
