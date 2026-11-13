package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// queryOptions are what a test's query is sent with.
type queryOptions struct {
	Format   string
	From, To time.Time
	Interval time.Duration
	Headers  map[string]string
	User     string
	Context  context.Context
}

// runQuery sends one query through QueryData, as Grafana does.
func runQuery(t *testing.T, datasource *Datasource, sql string, options queryOptions) backend.DataResponse {
	t.Helper()
	model, err := json.Marshal(map[string]any{"rawSql": sql, "format": options.Format})
	if err != nil {
		t.Fatal(err)
	}
	if options.To.IsZero() {
		options.To = time.Now()
	}
	if options.From.IsZero() {
		options.From = options.To.Add(-time.Hour)
	}
	if options.Interval == 0 {
		options.Interval = time.Minute
	}
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	request := &backend.QueryDataRequest{
		PluginContext: backend.PluginContext{Namespace: "default", PluginID: PluginID},
		Headers:       options.Headers,
		Queries: []backend.DataQuery{{RefID: "A", JSON: model, Interval: options.Interval, MaxDataPoints: 1000,
			TimeRange: backend.TimeRange{From: options.From, To: options.To}}},
	}
	if options.User != "" {
		request.PluginContext.User = &backend.User{Login: options.User}
	}
	response, err := datasource.QueryData(ctx, request)
	if err != nil {
		t.Fatalf("QueryData: %v", err)
	}
	result, ok := response.Responses["A"]
	if !ok {
		t.Fatal("QueryData gave no response for query A")
	}
	return result
}

// mustQuery runs a query that has to succeed and returns its frame.
func mustQuery(t *testing.T, datasource *Datasource, sql string, options queryOptions) *data.Frame {
	t.Helper()
	result := runQuery(t, datasource, sql, options)
	if result.Error != nil {
		t.Fatalf("query failed (%d, %s): %v\nSQL: %s", result.Status, result.ErrorSource, result.Error, sql)
	}
	if len(result.Frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(result.Frames))
	}
	return result.Frames[0]
}

// mustExec runs statements for their effect.
func mustExec(t *testing.T, datasource *Datasource, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		mustQuery(t, datasource, statement, queryOptions{})
	}
}

// wantFailure checks a failed query's status, source and message.
func wantFailure(t *testing.T, result backend.DataResponse, status backend.Status, source backend.ErrorSource, messageParts ...string) {
	t.Helper()
	if result.Error == nil {
		t.Fatalf("the query succeeded; want status %d", status)
	}
	if result.Status != status || result.ErrorSource != source {
		t.Errorf("got status %d from %q, want %d from %q (error: %v)", result.Status, result.ErrorSource, status, source, result.Error)
	}
	for _, part := range messageParts {
		if !strings.Contains(result.Error.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, result.Error)
		}
	}
}

// cell is row's value of a field: nil for a NULL, else the value itself (not
// a pointer to it).
func cell(field *data.Field, row int) any {
	value, ok := field.ConcreteAt(row)
	if !ok {
		return nil
	}
	return value
}

func describeFrame(frame *data.Frame) string {
	text, err := frame.StringTable(-1, -1)
	if err != nil {
		return fmt.Sprintf("(%v)", err)
	}
	return text
}
