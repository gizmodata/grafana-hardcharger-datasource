package plugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// A failed query is reported with the HTTP status that says whose it is to
// fix, and with Grafana's error source: "downstream" for everything that is
// the query's, the configuration's or the server's doing, "plugin" only for
// a fault of this plugin (or of the driver in it).
//
//	400  the SQL, a macro, the settings        downstream
//	401  the server refused the credentials    downstream
//	403  the server denied the statement       downstream
//	408  the query ran into its time limit     downstream
//	499  Grafana stopped waiting               downstream
//	502  the server could not be reached, or
//	     reported an internal error            downstream
//	500  anything else                         plugin
//	501  an Arrow type without a mapping       plugin

// statusRequestTimeout and statusClientClosed are HTTP statuses the SDK has
// no name for.
const (
	statusRequestTimeout backend.Status = 408
	statusClientClosed   backend.Status = 499
)

// queryError is a failure with its status, source and a message that is safe
// to show.
type queryError struct {
	Status  backend.Status
	Source  backend.ErrorSource
	Message string
}

func (e *queryError) Error() string { return e.Message }

func userError(format string, args ...any) *queryError {
	return &queryError{Status: backend.StatusBadRequest, Source: backend.ErrorSourceDownstream,
		Message: fmt.Sprintf(format, args...)}
}

// errorDetailCode is the detail the driver attaches to an error the server
// answered with: its Chargewire error code, such as "sql_error".
const errorDetailCode = "chargewire.code"

var (
	// driverPrefix is the driver's own tag and operation in front of the
	// server's message of a failed statement.
	driverPrefix = regexp.MustCompile(`^\[(?:chargewire|hardcharger)\] (?:ExecuteQuery: )?(?:the statement failed: )?`)
	// uriCredentials matches user:password@ in anything that looks like a URI.
	uriCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s@"']+@`)
)

// redactor removes a data source's secrets from text meant for people or
// logs.
type redactor struct {
	secrets []string
}

func (r redactor) redact(text string) string {
	for _, secret := range r.secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return uriCredentials.ReplaceAllString(text, "${1}[redacted]@")
}

// classify turns any error of running a query into a queryError. timeout is
// the query's own time limit, for the message of a query that ran into it.
func (r redactor) classify(ctx context.Context, err error, timeoutMessage string) *queryError {
	var known *queryError
	if errors.As(err, &known) {
		return &queryError{Status: known.Status, Source: known.Source, Message: r.redact(known.Message)}
	}
	var macro *macroError
	if errors.As(err, &macro) {
		return userError("%s", r.redact(macro.Error()))
	}
	var unsupported *unsupportedTypeError
	if errors.As(err, &unsupported) {
		return &queryError{Status: backend.StatusNotImplemented, Source: backend.ErrorSourcePlugin,
			Message: r.redact(unsupported.Error())}
	}

	// The context first: a deadline or a cancel surfaces through the driver
	// as whichever request it interrupted.
	if ctxErr := ctx.Err(); ctxErr != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return &queryError{Status: statusRequestTimeout, Source: backend.ErrorSourceDownstream,
				Message: timeoutMessage}
		}
		return &queryError{Status: statusClientClosed, Source: backend.ErrorSourceDownstream,
			Message: "the query was cancelled"}
	}

	var adbcErr adbc.Error
	if !errors.As(err, &adbcErr) {
		return &queryError{Status: backend.StatusInternal, Source: backend.ErrorSourcePlugin,
			Message: r.redact(err.Error())}
	}
	message := r.redact(driverPrefix.ReplaceAllString(adbcErr.Msg, ""))
	downstream := func(status backend.Status) *queryError {
		return &queryError{Status: status, Source: backend.ErrorSourceDownstream, Message: message}
	}
	switch adbcErr.Code {
	case adbc.StatusInvalidArgument, adbc.StatusNotFound, adbc.StatusAlreadyExists, adbc.StatusIntegrity,
		adbc.StatusInvalidState, adbc.StatusInvalidData, adbc.StatusNotImplemented:
		return downstream(backend.StatusBadRequest)
	case adbc.StatusUnauthenticated:
		return downstream(backend.StatusUnauthorized)
	case adbc.StatusUnauthorized:
		return downstream(backend.StatusForbidden)
	case adbc.StatusTimeout:
		return downstream(statusRequestTimeout)
	case adbc.StatusCancelled:
		return downstream(statusClientClosed)
	case adbc.StatusIO:
		return downstream(backend.StatusBadGateway)
	case adbc.StatusInternal:
		// The server's own internal_error is the server's; anything else here
		// is an answer the driver could not make sense of.
		if errorDetail(adbcErr, errorDetailCode) == "internal_error" {
			return downstream(backend.StatusBadGateway)
		}
	}
	return &queryError{Status: backend.StatusInternal, Source: backend.ErrorSourcePlugin, Message: message}
}

// errorDetail reads one of the details the driver attached to an error.
func errorDetail(err adbc.Error, key string) string {
	for _, detail := range err.Details {
		if detail.Key() != key {
			continue
		}
		if value, serr := detail.Serialize(); serr == nil {
			return string(value)
		}
	}
	return ""
}

// response is the Grafana response of a failed query.
func (e *queryError) response() backend.DataResponse {
	return backend.DataResponse{
		Error:       errors.New(e.Message),
		Status:      e.Status,
		ErrorSource: e.Source,
	}
}
