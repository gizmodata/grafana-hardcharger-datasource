package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestClassify(t *testing.T) {
	serverError := func(code adbc.Status, chargewireCode string) error {
		return adbc.Error{Code: code, Msg: "[chargewire] ExecuteQuery: the server's message (" + chargewireCode + ")",
			Details: []adbc.ErrorDetail{&adbc.TextErrorDetail{Name: "chargewire.code", Detail: chargewireCode}}}
	}
	cases := []struct {
		name   string
		err    error
		status backend.Status
		source backend.ErrorSource
		says   string
	}{
		{"a SQL error", serverError(adbc.StatusInvalidArgument, "sql_error"), 400, backend.ErrorSourceDownstream, "the server's message"},
		{"a missing table", serverError(adbc.StatusNotFound, "sql_error"), 400, backend.ErrorSourceDownstream, ""},
		{"a constraint", serverError(adbc.StatusIntegrity, "execution_error"), 400, backend.ErrorSourceDownstream, ""},
		{"an existing table", serverError(adbc.StatusAlreadyExists, "sql_error"), 400, backend.ErrorSourceDownstream, ""},
		{"wrong credentials", serverError(adbc.StatusUnauthenticated, "invalid_credentials"), 401, backend.ErrorSourceDownstream, ""},
		{"permission denied", serverError(adbc.StatusUnauthorized, "permission_denied"), 403, backend.ErrorSourceDownstream, ""},
		{"the server's statement timeout", serverError(adbc.StatusTimeout, "statement_timeout"), 408, backend.ErrorSourceDownstream, ""},
		{"cancelled on the server", serverError(adbc.StatusCancelled, "statement_cancelled"), 499, backend.ErrorSourceDownstream, ""},
		{"the server is draining", serverError(adbc.StatusIO, "server_draining"), 502, backend.ErrorSourceDownstream, ""},
		{"the server's internal error", serverError(adbc.StatusInternal, "internal_error"), 502, backend.ErrorSourceDownstream, ""},
		{"an answer the driver could not read", serverError(adbc.StatusInternal, "client_protocol_error"), 500, backend.ErrorSourcePlugin, ""},
		{"an internal error without a code", adbc.Error{Code: adbc.StatusInternal, Msg: "boom"}, 500, backend.ErrorSourcePlugin, "boom"},
		{"an unknown status", adbc.Error{Code: adbc.StatusUnknown, Msg: "what"}, 500, backend.ErrorSourcePlugin, ""},
		{"an error that is not the driver's", errors.New("a bug"), 500, backend.ErrorSourcePlugin, "a bug"},
		{"a wrapped driver error", fmt.Errorf("column x: %w", serverError(adbc.StatusInvalidArgument, "execution_error")), 400,
			backend.ErrorSourceDownstream, ""},
		{"a macro error", macroErrorf("unknown macro $__x"), 400, backend.ErrorSourceDownstream, "unknown macro $__x"},
		{"an unmapped type", &unsupportedTypeError{column: "c", dataType: "duration[us]"}, 501, backend.ErrorSourcePlugin, "duration[us]"},
		{"a user error", userError("no time column"), 400, backend.ErrorSourceDownstream, "no time column"},
		{"a deadline the driver reports", fmt.Errorf("reading: %w", context.DeadlineExceeded), 408, backend.ErrorSourceDownstream, "took too long"},
		{"a cancel the driver reports", fmt.Errorf("reading: %w", context.Canceled), 499, backend.ErrorSourceDownstream, "cancelled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactor{}.classify(context.Background(), c.err, "took too long")
			if got.Status != c.status || got.Source != c.source {
				t.Errorf("got %d from %q, want %d from %q (%s)", got.Status, got.Source, c.status, c.source, got.Message)
			}
			if !strings.Contains(got.Message, c.says) {
				t.Errorf("message %q does not say %q", got.Message, c.says)
			}
			if strings.Contains(got.Message, "[chargewire]") || strings.Contains(got.Message, "ExecuteQuery:") {
				t.Errorf("message %q keeps the driver's prefix", got.Message)
			}
			response := got.response()
			if response.Status != c.status || response.ErrorSource != c.source || response.Error == nil {
				t.Errorf("response: %+v", response)
			}
		})
	}
}

// A context that is over decides the status, whatever error the driver made
// of it.
func TestClassifyByContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	got := redactor{}.classify(cancelled, adbc.Error{Code: adbc.StatusIO, Msg: "connection reset"}, "")
	if got.Status != 499 || got.Source != backend.ErrorSourceDownstream {
		t.Errorf("a cancelled context: %d %q", got.Status, got.Source)
	}
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	got = redactor{}.classify(expired, adbc.Error{Code: adbc.StatusIO, Msg: "connection reset"}, "too slow")
	if got.Status != 408 || got.Message != "too slow" {
		t.Errorf("an expired context: %d %q", got.Status, got.Message)
	}
}

func TestRedaction(t *testing.T) {
	redact := redactor{secrets: []string{"hunter2", "tok-abc.def", "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----"}}
	cases := map[string]string{
		"password hunter2 refused":                                             "password [redacted] refused",
		"token tok-abc.def and hunter2 again":                                  "token [redacted] and [redacted] again",
		"key -----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY----- end": "key [redacted] end",
		`Get "https://joe:other-pw@db.example.com/chargewire": EOF`:            `Get "https://[redacted]@db.example.com/chargewire": EOF`,
		"hardcharger://joe@h:31337 and http://127.0.0.1:1/chargewire":          "hardcharger://[redacted]@h:31337 and http://127.0.0.1:1/chargewire",
		"nothing to hide: SELECT 'a@b.example' FROM t":                         "nothing to hide: SELECT 'a@b.example' FROM t",
	}
	for input, want := range cases {
		if got := redact.redact(input); got != want {
			t.Errorf("redact(%q)\n got %q\nwant %q", input, got, want)
		}
	}
	// Through classify, for every kind of error.
	for _, err := range []error{
		adbc.Error{Code: adbc.StatusUnauthenticated, Msg: "[chargewire] signing in with hunter2 failed"},
		errors.New("plain hunter2"),
		userError("settings hunter2"),
		macroErrorf("macro hunter2"),
	} {
		if got := redact.classify(context.Background(), err, ""); strings.Contains(got.Message, "hunter2") {
			t.Errorf("classify leaked a secret: %q", got.Message)
		}
	}
}
