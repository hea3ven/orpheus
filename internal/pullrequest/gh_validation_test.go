package pullrequest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
)

func TestGHProviderStatusByURLRejectsMalformedURL(t *testing.T) {
	_, err := pullrequest.GHProvider{}.StatusByURL(
		context.Background(),
		pullrequest.StatusByURLRequest{URL: "not-a-url"},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v, want invalid URL", err)
	}
}
