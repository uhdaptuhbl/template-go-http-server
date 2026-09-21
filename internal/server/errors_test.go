package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
)

// TestWriteJSONError covers AC-002.1, AC-002.4, and AC-002.21.
func TestWriteJSONError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		code     string
		message  string
		wantBody string
	}{
		{
			name:     "internal error",
			status:   http.StatusInternalServerError,
			code:     errorCodeInternal,
			message:  "",
			wantBody: `{"errors":[{"status":"500","code":"internal","title":"Internal server error"}]}`,
		},
		{
			name:     "payload too large",
			status:   http.StatusRequestEntityTooLarge,
			code:     errorCodePayloadTooLarge,
			message:  "",
			wantBody: `{"errors":[{"status":"413","code":"payload_too_large","title":"Request body too large"}]}`,
		},
		{
			name:     "overloaded",
			status:   http.StatusServiceUnavailable,
			code:     errorCodeOverloaded,
			message:  "",
			wantBody: `{"errors":[{"status":"503","code":"overloaded","title":"Server overloaded"}]}`,
		},
		{
			// A detail that says more than the title survives; the title
			// names the kind of failure and the detail names this one.
			name:     "detail that adds something",
			status:   http.StatusBadRequest,
			code:     errorCodeInvalidBody,
			message:  `field "age" must be a JSON number`,
			wantBody: `{"errors":[{"status":"400","code":"invalid_body","title":"Invalid request body","detail":"field \"age\" must be a JSON number"}]}`,
		},
		{
			// A detail that only restates the title is dropped rather than
			// sent twice under two member names.
			name:     "detail that restates the title",
			status:   http.StatusServiceUnavailable,
			code:     errorCodeOverloaded,
			message:  "Server Overloaded",
			wantBody: `{"errors":[{"status":"503","code":"overloaded","title":"Server overloaded"}]}`,
		},
		{
			// A code nobody gave a title to still produces a well-formed
			// document, with the code standing in.
			name:     "code with no title",
			status:   http.StatusTeapot,
			code:     "no_such_code",
			message:  "",
			wantBody: `{"errors":[{"status":"418","code":"no_such_code","title":"no_such_code"}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()

			writeJSONError(recorder, zap.NewNop(), test.status, test.code, test.message)

			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("got Content-Type %q, want %q", got, "application/json")
			}

			got := strings.TrimSpace(recorder.Body.String())
			if diff := cmp.Diff(test.wantBody, got); diff != "" {
				t.Errorf("body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
