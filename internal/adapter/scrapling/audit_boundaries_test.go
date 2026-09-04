package scrapling_test

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/scrapling"
)

func TestAuditExtractReturnsDeniedRedirect(t *testing.T) {
	r := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{"make_request": map[string]any{"status": 200, "url": "http://127.0.0.1/private", "content": []string{"synthetic-private-data"}}})})
	out, err := r.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest, map[string]any{"url": "https://shop.example/", "fields": fields("value", "body")}))
	if err == nil || out.Result != nil {
		t.Fatalf("expected refusal without content: %+v %v", out, err)
	}
}
