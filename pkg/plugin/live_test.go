package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/gizmodata/hardcharger-adbc/go/v2/hardcharger"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"

	"github.com/gizmodata/grafana-hardcharger-datasource/pkg/models"
)

// uniqueTag makes a statement findable in the server's list of statements:
// name, and a suffix no other run of the test has (the servers are shared by
// every test of the process, and by every -count iteration).
func uniqueTag(name string) string {
	return fmt.Sprintf("%s-%d", name, tagCounter.Add(1)+time.Now().UnixNano())
}

var tagCounter atomic.Int64

// endlessQuery never finishes on its own within a test: only a cancel ends
// it. tag makes it findable in the server's list of statements.
func endlessQuery(tag string) string {
	return "SELECT count(*) FROM range(100000000000000) t(i) WHERE hash(i) % 7 = 3 /* " + tag + " */"
}

// statementStatus reads, from the server's own record, the status and
// cancel reason of the statement whose SQL contains tag ("" when it has none
// yet).
func statementStatus(t *testing.T, ds *Datasource, tag string) (status, cancelReason, queryTag string) {
	t.Helper()
	frame := mustQuery(t, ds, "SELECT status, cancel_reason, query_tag FROM hardcharger_live_queries() WHERE sql_text LIKE '%/* "+
		tag+" */%' AND sql_text NOT LIKE '%hardcharger_live_queries%'", queryOptions{})
	if frame.Rows() == 0 {
		return "", "", ""
	}
	if frame.Rows() > 1 {
		t.Fatalf("%d statements carry the tag %s", frame.Rows(), tag)
	}
	text := func(index int) string {
		value, _ := cell(frame.Fields[index], 0).(string)
		return value
	}
	return text(0), text(1), text(2)
}

// waitForStatus polls the server until the tagged statement has the status.
func waitForStatus(t *testing.T, ds *Datasource, tag, want string) (cancelReason string) {
	t.Helper()
	deadline := time.Now().Add(scaled(20 * time.Second))
	var status string
	for time.Now().Before(deadline) {
		status, cancelReason, _ = statementStatus(t, ds, tag)
		if status == want {
			return cancelReason
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the statement %s is %q on the server, not %q", tag, status, want)
	return ""
}

func TestLiveHealthCheck(t *testing.T) {
	server := plainServer(t)
	ds := newLiveDatasource(t, server, nil, nil)
	health, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != backend.HealthStatusOk {
		t.Fatalf("health: %v %s", health.Status, health.Message)
	}
	if !regexp.MustCompile(`^Connected to Hardcharger \d+\.\d+\.\d+\S* \(DuckDB v\d+\.\d+\.\d+\S*\)\. Catalog: memory, schema: main\.$`).MatchString(health.Message) {
		t.Errorf("message: %q", health.Message)
	}
	var details healthInfo
	if err := json.Unmarshal(health.JSONDetails, &details); err != nil {
		t.Fatal(err)
	}
	// The version is the server's own: what its SQL says too.
	frame := mustQuery(t, ds, "SELECT hardcharger_version, duckdb_version FROM hardcharger_server_info()", queryOptions{})
	serverVersion, duckdbVersion := cell(frame.Fields[0], 0).(string), cell(frame.Fields[1], 0).(string)
	if details.Server != "Hardcharger" || !strings.HasPrefix(details.Version, serverVersion) || details.DuckDB != duckdbVersion ||
		details.Catalog != "memory" || details.Schema != "main" {
		t.Errorf("details %+v; the server says %s and %s", details, serverVersion, duckdbVersion)
	}
	// It ran a real statement on the server, tagged as the health check.
	frame = mustQuery(t, ds, "SELECT count(*) FROM hardcharger_live_queries() WHERE sql_text LIKE 'SELECT version() AS duckdb%' "+
		"AND status = 'success' AND query_tag LIKE '%\"source\":\"health_check\"%'", queryOptions{})
	if count := cell(frame.Fields[0], 0).(int64); count < 1 {
		t.Errorf("the server recorded %d health check statements", count)
	}
}

func TestLiveHealthCheckFailures(t *testing.T) {
	server := plainServer(t)
	const wrongPassword = "not-the-password-4711"
	cases := []struct {
		name string
		ds   *Datasource
		want []string
	}{
		{"a wrong password", newLiveDatasource(t, server, nil, map[string]string{"password": wrongPassword}),
			[]string{"The server refused the credentials", "the user name or password is not correct", "invalid_credentials"}},
		{"an unknown user", newLiveDatasource(t, server, map[string]any{"username": "nobody"}, nil),
			[]string{"The server refused the credentials", "invalid_credentials"}},
		{"a token the server never issued", newLiveDatasource(t, server, map[string]any{"authType": "token"},
			map[string]string{"token": "not-a-token-0815"}), []string{"The server refused the credentials"}},
		{"a catalog that does not exist", newLiveDatasource(t, server, map[string]any{"catalog": "no_such_catalog"}, nil),
			[]string{`the data source's default catalog "no_such_catalog" could not be used`}},
		{"a schema that does not exist", newLiveDatasource(t, server, map[string]any{"schema": "no_such_schema"}, nil),
			[]string{`the data source's default schema "no_such_schema" could not be used`}},
		{"TLS to a server without it", newLiveDatasource(t, server, map[string]any{"useTLS": true}, nil),
			[]string{"The server could not be reached"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			health, err := c.ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if health.Status != backend.HealthStatusError {
				t.Fatalf("health: %v %s", health.Status, health.Message)
			}
			for _, want := range c.want {
				if !strings.Contains(health.Message, want) {
					t.Errorf("message %q does not say %q", health.Message, want)
				}
			}
			for _, secret := range []string{wrongPassword, "not-a-token-0815", server.Password} {
				if strings.Contains(health.Message, secret) {
					t.Errorf("the message repeats a secret: %q", health.Message)
				}
			}
		})
	}
}

// Whose error it is: the SQL's and the configuration's are 4xx and
// "downstream", with the server's own message.
func TestLiveErrors(t *testing.T) {
	server := plainServer(t)
	ds := newLiveDatasource(t, server, nil, nil)
	mustExec(t, ds, "CREATE OR REPLACE TABLE live_errors (id INTEGER PRIMARY KEY, s VARCHAR)",
		"INSERT INTO live_errors VALUES (1, 'one'), (2, 'not a number')")
	cases := []struct {
		name   string
		ds     *Datasource
		sql    string
		status backend.Status
		want   []string
	}{
		{"a syntax error", ds, "SELEC 1", 400, []string{`Parser Error: syntax error at or near "SELEC"`, "sql_error"}},
		{"a table that does not exist", ds, "SELECT * FROM no_such_table", 400,
			[]string{"Catalog Error: Table with name no_such_table does not exist", "sql_error"}},
		{"a column that does not exist", ds, "SELECT nope FROM live_errors", 400, []string{`Binder Error: Referenced column "nope" not found`}},
		{"a conversion that fails while the rows are read", ds, "SELECT CAST(s AS INTEGER) FROM live_errors ORDER BY id", 400,
			[]string{"Conversion Error: Could not convert string 'one' to INT32", "execution_error"}},
		{"a constraint", ds, "INSERT INTO live_errors VALUES (1, 'again')", 400, []string{"Constraint Error", "primary key"}},
		{"a table that exists already", ds, "CREATE TABLE live_errors (x INTEGER)", 400, []string{`Table with name "live_errors" already exists`}},
		{"a wrong password", newLiveDatasource(t, server, nil, map[string]string{"password": "wrong-password-0815"}), "SELECT 1", 401,
			[]string{"the user name or password is not correct", "invalid_credentials"}},
		{"a default catalog that does not exist", newLiveDatasource(t, server, map[string]any{"catalog": "no_such_catalog"}, nil),
			"SELECT 1", 400, []string{`the data source's default catalog "no_such_catalog" could not be used`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := runQuery(t, c.ds, c.sql, queryOptions{})
			wantFailure(t, result, c.status, backend.ErrorSourceDownstream, c.want...)
			message := result.Error.Error()
			if strings.Contains(message, "[chargewire]") || strings.Contains(message, "wrong-password-0815") ||
				strings.Contains(message, server.Password) {
				t.Errorf("the message keeps the driver's tag or a secret: %q", message)
			}
		})
	}
	// The positive twin: the same data source runs a correct statement.
	if frame := mustQuery(t, ds, "SELECT s FROM live_errors WHERE id = 1", queryOptions{}); cell(frame.Fields[0], 0) != "one" {
		t.Errorf("got\n%s", describeFrame(frame))
	}
}

func TestLiveStatementsWithoutRows(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	frame := mustQuery(t, ds, "CREATE OR REPLACE TABLE live_ddl (a INTEGER, b VARCHAR)", queryOptions{})
	if frame.Rows() != 0 {
		t.Errorf("DDL returned rows:\n%s", describeFrame(frame))
	}
	mustExec(t, ds, "INSERT INTO live_ddl VALUES (1, 'x'), (2, NULL)")
	// A result without rows keeps its columns and their types.
	frame = mustQuery(t, ds, "SELECT a, b FROM live_ddl WHERE a > 100", queryOptions{})
	if frame.Rows() != 0 || len(frame.Fields) != 2 || frame.Fields[0].Type() != data.FieldTypeNullableInt32 ||
		frame.Fields[1].Type() != data.FieldTypeNullableString {
		t.Errorf("an empty result:\n%s", describeFrame(frame))
	}
	// Several statements in one query: the last one's rows.
	frame = mustQuery(t, ds, "INSERT INTO live_ddl VALUES (3, 'y'); SELECT count(*) AS n FROM live_ddl", queryOptions{})
	if cell(frame.Fields[0], 0) != int64(3) {
		t.Errorf("got\n%s", describeFrame(frame))
	}
	if frame.Meta == nil || frame.Meta.ExecutedQueryString == "" || frame.RefID != "A" {
		t.Errorf("meta %+v, refId %q", frame.Meta, frame.RefID)
	}
}

// The row cap is applied while reading: the plugin stops at the cap, says so
// on the frame, and the server's statement ends instead of running on.
func TestLiveRowLimit(t *testing.T) {
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	limited := newLiveDatasource(t, server, map[string]any{"maxRows": 1000}, nil)

	started := time.Now()
	tag := uniqueTag("live-row-limit")
	frame := mustQuery(t, limited, "SELECT i, 'row ' || i AS s FROM range(100000000000) t(i) /* "+tag+" */", queryOptions{})
	elapsed := time.Since(started)
	if frame.Rows() != 1000 {
		t.Fatalf("got %d rows, want 1000", frame.Rows())
	}
	if cell(frame.Fields[0], 0) != int64(0) || cell(frame.Fields[0], 999) != int64(999) || cell(frame.Fields[1], 999) != "row 999" {
		t.Errorf("the first 1000 rows are not rows 0 to 999")
	}
	if len(frame.Meta.Notices) != 1 || frame.Meta.Notices[0].Severity != data.NoticeSeverityWarning ||
		!strings.Contains(frame.Meta.Notices[0].Text, "cut off at 1000 rows") {
		t.Errorf("notices: %+v", frame.Meta.Notices)
	}
	// 10^11 rows can't be read in this time: the plugin stopped.
	if elapsed > scaled(20*time.Second) {
		t.Errorf("the capped query took %s", elapsed)
	}
	// And so did the server: the statement is cancelled, not still producing rows.
	waitForStatus(t, admin, tag, "cancelled")

	// Exactly the cap, and one under and over it.
	for rows, truncated := range map[int]bool{999: false, 1000: false, 1001: true} {
		frame := mustQuery(t, limited, fmt.Sprintf("SELECT i FROM range(%d) t(i)", rows), queryOptions{})
		wantRows := min(rows, 1000)
		if frame.Rows() != wantRows || (len(frame.Meta.Notices) > 0) != truncated {
			t.Errorf("%d rows: got %d rows and notices %+v", rows, frame.Rows(), frame.Meta.Notices)
		}
	}
	// The default cap.
	frame = mustQuery(t, admin, "SELECT i FROM range(100001) t(i)", queryOptions{})
	if frame.Rows() != models.DefaultMaxRows || len(frame.Meta.Notices) != 1 {
		t.Errorf("default cap: %d rows, notices %+v", frame.Rows(), frame.Meta.Notices)
	}
}

// A query that runs into the data source's time limit is cancelled on the
// server, and reported as the query's (408, downstream).
func TestLiveQueryTimeoutCancelsOnTheServer(t *testing.T) {
	tag := uniqueTag("live-timeout")
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	impatient := newLiveDatasource(t, server, map[string]any{"queryTimeout": 1.5}, nil)

	started := time.Now()
	result := runQuery(t, impatient, endlessQuery(tag), queryOptions{})
	elapsed := time.Since(started)
	wantFailure(t, result, 408, backend.ErrorSourceDownstream, "ran longer than the data source's query timeout (1.5s)",
		"cancelled on the server")
	if elapsed < 1400*time.Millisecond || elapsed > scaled(15*time.Second) {
		t.Errorf("the query returned after %s", elapsed)
	}
	// The server's own record: the statement ran, and was cancelled.
	reason := waitForStatus(t, admin, tag, "cancelled")
	if reason != "client" && reason != "session_closed" {
		t.Errorf("cancel reason %q", reason)
	}
	// The data source still works afterwards.
	if frame := mustQuery(t, impatient, "SELECT 42 AS answer", queryOptions{}); cell(frame.Fields[0], 0) != int32(42) {
		t.Errorf("after the timeout:\n%s", describeFrame(frame))
	}
}

// When Grafana cancels the request (the user left the dashboard), the
// statement stops on the server. The control: until the cancel, the server
// shows the same statement running.
func TestLiveContextCancelStopsTheStatement(t *testing.T) {
	tag := uniqueTag("live-cancel")
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	ds := newLiveDatasource(t, server, map[string]any{"queryTimeout": 0}, nil) // no time limit: only the cancel ends it

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan backend.DataResponse, 1)
	go func() { finished <- runQuery(t, ds, endlessQuery(tag), queryOptions{Context: ctx}) }()

	waitForStatus(t, admin, tag, "executing")
	// It keeps running while nobody cancels it.
	time.Sleep(scaled(1500 * time.Millisecond))
	select {
	case result := <-finished:
		t.Fatalf("the query ended by itself: %v", result.Error)
	default:
	}
	if status, _, _ := statementStatus(t, admin, tag); status != "executing" {
		t.Fatalf("before the cancel the statement is %q", status)
	}

	cancelled := time.Now()
	cancel()
	select {
	case result := <-finished:
		wantFailure(t, result, 499, backend.ErrorSourceDownstream, "the query was cancelled")
	case <-time.After(scaled(15 * time.Second)):
		t.Fatal("the query did not return after its context was cancelled")
	}
	if waited := time.Since(cancelled); waited > scaled(10*time.Second) {
		t.Errorf("returning took %s", waited)
	}
	waitForStatus(t, admin, tag, "cancelled")
}

// The server's own statement timeout is set with each statement, a little
// after the plugin's time limit. Here the margin is turned around, so the
// server's limit comes first: the server ends the statement and says why.
func TestLiveServerStatementTimeout(t *testing.T) {
	tag := uniqueTag("live-server-timeout")
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	ds := newLiveDatasource(t, server, map[string]any{"queryTimeout": 30}, nil)
	previous := serverTimeoutMargin
	serverTimeoutMargin = -29 * time.Second // the server's limit: 1 s
	defer func() { serverTimeoutMargin = previous }()

	started := time.Now()
	result := runQuery(t, ds, endlessQuery(tag), queryOptions{})
	wantFailure(t, result, 408, backend.ErrorSourceDownstream, "statement_timeout")
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed > scaled(15*time.Second) {
		t.Errorf("the server's timeout of 1 s ended the query after %s", elapsed)
	}
	if strings.Contains(result.Error.Error(), "the data source's query timeout") {
		t.Errorf("the plugin's own limit fired, not the server's: %v", result.Error)
	}
	waitForStatus(t, admin, tag, "timeout")
}

func TestLiveMacros(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	mustExec(t, ds,
		"CREATE OR REPLACE TABLE live_macros (ts TIMESTAMP, tstz TIMESTAMPTZ, d DATE, epoch BIGINT, host VARCHAR, v DOUBLE)",
		`INSERT INTO live_macros
		 SELECT ts, ts AT TIME ZONE 'UTC', CAST(ts AS DATE), epoch(ts)::BIGINT, host, v FROM (VALUES
		   (TIMESTAMP '2026-03-01 09:59:59', 'a', 1.0),
		   (TIMESTAMP '2026-03-01 10:00:00', 'a', 2.0),
		   (TIMESTAMP '2026-03-01 10:02:30', 'b', 3.0),
		   (TIMESTAMP '2026-03-01 10:07:00', 'a', 4.0),
		   (TIMESTAMP '2026-03-01 10:30:00', 'b', 5.0),
		   (TIMESTAMP '2026-03-01 10:30:01', 'a', 6.0)) t(ts, host, v)`)
	window := queryOptions{From: utc(2026, 3, 1, 10, 0, 0, 0), To: utc(2026, 3, 1, 10, 30, 0, 0), Interval: 5 * time.Minute}
	sum := func(sql string) any {
		t.Helper()
		frame := mustQuery(t, ds, sql, window)
		return cell(frame.Fields[0], 0)
	}
	// The range is inclusive at both ends: rows 2 to 5, whatever the column's type.
	for _, column := range []string{"ts", "tstz"} {
		if got := sum("SELECT sum(v) FROM live_macros WHERE $__timeFilter(" + column + ")"); got != 14.0 {
			t.Errorf("$__timeFilter(%s): sum %v, want 14", column, got)
		}
		if got := sum("SELECT sum(v) FROM live_macros WHERE " + column + " >= $__timeFrom() AND " + column + " < $__timeTo()"); got != 9.0 {
			t.Errorf("$__timeFrom()/$__timeTo() on %s: sum %v, want 9", column, got)
		}
		if got := sum("SELECT sum(v) FROM live_macros WHERE " + column + " BETWEEN $__timeFrom AND $__timeTo"); got != 14.0 {
			t.Errorf("$__timeFrom/$__timeTo without parentheses on %s: sum %v, want 14", column, got)
		}
	}
	if got := sum("SELECT sum(v) FROM live_macros WHERE $__unixEpochFilter(epoch)"); got != 14.0 {
		t.Errorf("$__unixEpochFilter: sum %v", got)
	}
	if got := sum("SELECT sum(v) FROM live_macros WHERE epoch >= $__unixEpochFrom() AND epoch < $__unixEpochTo()"); got != 9.0 {
		t.Errorf("$__unixEpochFrom/To: sum %v", got)
	}
	if got := sum("SELECT sum(v) FROM live_macros WHERE epoch * 1000 BETWEEN $__from AND $__to"); got != 14.0 {
		t.Errorf("$__from/$__to: sum %v", got)
	}
	// A DATE column: the whole day is in range of a window inside it.
	day := queryOptions{From: utc(2026, 3, 1, 0, 0, 0, 0), To: utc(2026, 3, 1, 23, 59, 59, 0)}
	if frame := mustQuery(t, ds, "SELECT count(*) FROM live_macros WHERE $__timeFilter(d)", day); cell(frame.Fields[0], 0) != int64(6) {
		t.Errorf("$__timeFilter on a DATE:\n%s", describeFrame(frame))
	}
	// A macro in a string, a comment or a quoted identifier reaches the server untouched.
	frame := mustQuery(t, ds, `SELECT '$__timeFilter(ts)' AS "$__timeTo()", $$ $__interval $$ AS d -- $__nope()
		FROM live_macros /* $__timeFrom() */ WHERE $__timeFilter(ts) LIMIT 1`, window)
	if frame.Fields[0].Name != "$__timeTo()" || cell(frame.Fields[0], 0) != "$__timeFilter(ts)" || cell(frame.Fields[1], 0) != " $__interval " {
		t.Errorf("literals:\n%s", describeFrame(frame))
	}
	// The interval, as text and in milliseconds.
	frame = mustQuery(t, ds, "SELECT '$__interval' AS quoted, $__interval_ms AS ms, to_milliseconds($__interval_ms) AS iv", window)
	if cell(frame.Fields[0], 0) != "$__interval" || cell(frame.Fields[1], 0) != int32(300000) || cell(frame.Fields[2], 0) != "00:05:00" {
		t.Errorf("$__interval_ms:\n%s", describeFrame(frame))
	}

	// $__timeGroup buckets on the server; the buckets are UTC-aligned whatever the session's time zone.
	for _, column := range []string{"ts", "tstz"} {
		frame := mustQuery(t, ds, "SELECT $__timeGroup("+column+", $__interval) AS time, sum(v) AS v FROM live_macros WHERE $__timeFilter("+
			column+") GROUP BY 1 ORDER BY 1", window)
		wantTimes := []time.Time{utc(2026, 3, 1, 10, 0, 0, 0), utc(2026, 3, 1, 10, 5, 0, 0), utc(2026, 3, 1, 10, 30, 0, 0)}
		wantValues := []float64{5, 4, 5}
		if frame.Rows() != 3 {
			t.Fatalf("$__timeGroup(%s):\n%s", column, describeFrame(frame))
		}
		for row := range wantTimes {
			if !cell(frame.Fields[0], row).(time.Time).Equal(wantTimes[row]) || cell(frame.Fields[1], row) != wantValues[row] {
				t.Errorf("$__timeGroup(%s) row %d: %v = %v", column, row, cell(frame.Fields[0], row), cell(frame.Fields[1], row))
			}
		}
	}
	// A day's bucket of a TIMESTAMPTZ starts at midnight UTC, not at the server's midnight.
	frame = mustQuery(t, ds, "SELECT DISTINCT $__timeGroup(tstz, 1d) AS time FROM live_macros", window)
	if frame.Rows() != 1 || !cell(frame.Fields[0], 0).(time.Time).Equal(utc(2026, 3, 1, 0, 0, 0, 0)) {
		t.Errorf("a day bucket:\n%s", describeFrame(frame))
	}
	// Months are calendar months.
	frame = mustQuery(t, ds, "SELECT DISTINCT $__timeGroup(ts, 1M) AS time FROM live_macros", window)
	if frame.Rows() != 1 || !cell(frame.Fields[0], 0).(time.Time).Equal(utc(2026, 3, 1, 0, 0, 0, 0)) {
		t.Errorf("a month bucket:\n%s", describeFrame(frame))
	}
	// A macro error is the query's (400), before anything reaches the server.
	wantFailure(t, runQuery(t, ds, "SELECT $__timeGroup(ts) FROM live_macros", window), 400, backend.ErrorSourceDownstream,
		"$__timeGroup takes a time column, an interval and an optional fill")
}

// The grid that gaps are filled on is the one the server's time_bucket
// uses, for every width: also those that 1970 and DuckDB's origin
// (2000-01-03) disagree on.
func TestLiveFillGridMatchesTimeBucket(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	for _, interval := range []string{"7s", "45s", "7m", "5h", "1d", "2d", "1w", "3w"} {
		// A range of some 150 buckets around the row, starting and ending inside a bucket.
		width := mustInterval(t, interval)
		row := utc(2026, 3, 15, 12, 34, 56, 0)
		window := queryOptions{Format: formatTimeSeries, From: row.Add(-70*width - 1234*time.Millisecond), To: row.Add(80*width + 777*time.Millisecond)}
		// One row in the middle of the range; everything else is filled.
		frame := mustQuery(t, ds, "SELECT $__timeGroup(ts, "+interval+", 0) AS time, count(*)::DOUBLE AS n FROM (SELECT TIMESTAMP "+
			"'2026-03-15 12:34:56' AS ts) GROUP BY 1", window)
		times := frame.Fields[0]
		bucket := mustQuery(t, ds, "SELECT time_bucket(INTERVAL '"+strings.NewReplacer("s", " seconds", "m", " minutes", "h", " hours",
			"d", " days", "w", " weeks").Replace(interval)+"', TIMESTAMP '2026-03-15 12:34:56') AS b", queryOptions{})
		want := cell(bucket.Fields[0], 0).(time.Time)
		found, ones := false, 0
		for row := 0; row < frame.Rows(); row++ {
			at := times.At(row).(time.Time)
			value := cell(frame.Fields[1], row)
			if at.Equal(want) {
				found = true
				if value != 1.0 {
					t.Errorf("%s: the bucket %s holds %v, not the row", interval, at, value)
				}
			} else if value != 0.0 {
				t.Errorf("%s: the filled point %s holds %v", interval, at, value)
			}
			if value == 1.0 {
				ones++
			}
		}
		if !found || ones != 1 {
			t.Errorf("%s: the server's bucket %s is not on the grid (first point %s, %d rows)", interval, want, times.At(0), frame.Rows())
		}
		if first := times.At(0).(time.Time); first.After(window.From) || window.From.Sub(first) >= mustInterval(t, interval) {
			t.Errorf("%s: the grid starts at %s for a range from %s", interval, first, window.From)
		}
	}
}

func mustInterval(t *testing.T, text string) time.Duration {
	t.Helper()
	interval, err := parseGroupInterval(text)
	if err != nil {
		t.Fatal(err)
	}
	return interval.duration
}

func TestLiveTimeSeries(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	mustExec(t, ds,
		"CREATE OR REPLACE TABLE live_series (ts TIMESTAMPTZ, host VARCHAR, region VARCHAR, cpu DOUBLE, mem BIGINT)",
		`INSERT INTO live_series VALUES
		   ('2026-03-01 10:10:00+00', 'b', 'eu', 0.4, 40),
		   ('2026-03-01 10:00:00+00', 'a', 'us', 0.1, 10),
		   ('2026-03-01 10:00:00+00', 'b', 'eu', 0.3, 30),
		   ('2026-03-01 10:05:00+00', 'a', 'us', NULL, 20),
		   ('2026-03-01 10:20:00+00', 'a', 'us', 0.2, NULL)`)
	window := queryOptions{Format: formatTimeSeries, From: utc(2026, 3, 1, 10, 0, 0, 0), To: utc(2026, 3, 1, 10, 20, 0, 0),
		Interval: 5 * time.Minute}

	// Long rows, unordered, two label columns, two value columns: one field per series and value.
	frame := mustQuery(t, ds, "SELECT host, ts, region, cpu, mem FROM live_series WHERE $__timeFilter(ts)", window)
	if frame.Meta.Type != data.FrameTypeTimeSeriesWide || frame.Rows() != 4 || len(frame.Fields) != 5 ||
		frame.Fields[0].Type() != data.FieldTypeTime {
		t.Fatalf("wide frame:\n%s", describeFrame(frame))
	}
	cpuA := wideField(t, frame, "cpu", data.Labels{"host": "a", "region": "us"})
	memB := wideField(t, frame, "mem", data.Labels{"host": "b", "region": "eu"})
	wantCPU := []any{0.1, nil, nil, 0.2}             // 10:00, 10:05 (NULL), 10:10 (no row), 10:20
	wantMem := []any{int64(30), nil, int64(40), nil} // b has rows at 10:00 and 10:10
	wantTimes := []int{0, 5, 10, 20}
	for row := range wantTimes {
		if at := frame.Fields[0].At(row).(time.Time); !at.Equal(utc(2026, 3, 1, 10, wantTimes[row], 0, 0)) {
			t.Errorf("row %d is at %s", row, at)
		}
		if cell(cpuA, row) != wantCPU[row] || cell(memB, row) != wantMem[row] {
			t.Errorf("row %d: cpu{a}=%v (want %v), mem{b}=%v (want %v)", row, cell(cpuA, row), wantCPU[row], cell(memB, row), wantMem[row])
		}
	}
	if frame.Meta.ExecutedQueryString == "" || strings.Contains(frame.Meta.ExecutedQueryString, "$__") {
		t.Errorf("the executed query: %q", frame.Meta.ExecutedQueryString)
	}

	// Grouped on the server and gaps filled over the whole range.
	for fill, want := range map[string][]any{
		"NULL":     {0.1, nil, nil, nil, 0.2},
		"0":        {0.1, 0.0, 0.0, 0.0, 0.2},
		"previous": {0.1, 0.1, 0.1, 0.1, 0.2},
	} {
		frame := mustQuery(t, ds, "SELECT $__timeGroup(ts, $__interval, "+fill+") AS time, host, avg(cpu) AS cpu FROM live_series "+
			"WHERE $__timeFilter(ts) AND host = 'a' AND cpu IS NOT NULL GROUP BY 1, 2", window)
		series := wideField(t, frame, "cpu", data.Labels{"host": "a"})
		if frame.Rows() != 5 {
			t.Fatalf("fill %s:\n%s", fill, describeFrame(frame))
		}
		for row := range want {
			if at := frame.Fields[0].At(row).(time.Time); !at.Equal(utc(2026, 3, 1, 10, row*5, 0, 0)) {
				t.Errorf("fill %s: row %d is at %s", fill, row, at)
			}
			if cell(series, row) != want[row] {
				t.Errorf("fill %s: row %d is %v, want %v", fill, row, cell(series, row), want[row])
			}
		}
	}

	// A Unix epoch column named time.
	frame = mustQuery(t, ds, "SELECT $__unixEpochGroupAlias(epoch(ts)::BIGINT, 10m), sum(mem) AS mem FROM live_series GROUP BY 1 ORDER BY 1", window)
	if frame.Fields[0].Type() != data.FieldTypeTime || frame.Rows() != 3 || !frame.Fields[0].At(1).(time.Time).Equal(utc(2026, 3, 1, 10, 10, 0, 0)) ||
		cell(frame.Fields[1], 0) != 60.0 && cell(frame.Fields[1], 0) != int64(60) {
		t.Errorf("an epoch time column:\n%s", describeFrame(frame))
	}
	// Wide rows stay as they are, in time order.
	frame = mustQuery(t, ds, "SELECT ts, cpu, mem FROM live_series WHERE host = 'a' ORDER BY ts DESC", window)
	if frame.Meta.Type != data.FrameTypeTimeSeriesWide || frame.Rows() != 3 || cell(frame.Fields[1], 0) != 0.1 || cell(frame.Fields[2], 2) != nil {
		t.Errorf("wide rows:\n%s", describeFrame(frame))
	}
	// No rows: an empty frame, not an error.
	frame = mustQuery(t, ds, "SELECT ts, host, cpu FROM live_series WHERE false", window)
	if frame.Rows() != 0 {
		t.Errorf("no rows:\n%s", describeFrame(frame))
	}
	// What the format cannot take is the query's error.
	wantFailure(t, runQuery(t, ds, "SELECT host, cpu FROM live_series", window), 400, backend.ErrorSourceDownstream, "needs a time column")
	wantFailure(t, runQuery(t, ds, "SELECT ts, host FROM live_series", window), 400, backend.ErrorSourceDownstream,
		"needs at least one number column")
	wantFailure(t, runQuery(t, ds, "SELECT NULL::TIMESTAMP AS ts, 1.0 AS v", window), 400, backend.ErrorSourceDownstream,
		`the time column "ts" is NULL in row 1`)
	// The same rows as a table are left alone.
	table := mustQuery(t, ds, "SELECT host, ts, cpu FROM live_series ORDER BY ts, host", queryOptions{Format: formatTable})
	if table.Rows() != 5 || len(table.Fields) != 3 || table.Fields[0].Name != "host" || table.Meta.Type != "" {
		t.Errorf("table format:\n%s", describeFrame(table))
	}
}

// The default catalog and schema: every query's session starts there.
func TestLiveCatalogAndSchema(t *testing.T) {
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	mustExec(t, admin,
		"ATTACH IF NOT EXISTS ':memory:' AS live_other",
		"CREATE SCHEMA IF NOT EXISTS live_other.reports",
		"CREATE SCHEMA IF NOT EXISTS memory.reports",
		"CREATE OR REPLACE TABLE live_other.reports.where_am_i AS SELECT 'live_other.reports' AS place",
		"CREATE OR REPLACE TABLE live_other.main.where_am_i AS SELECT 'live_other.main' AS place",
		"CREATE OR REPLACE TABLE memory.reports.where_am_i AS SELECT 'memory.reports' AS place",
		"CREATE OR REPLACE TABLE memory.main.where_am_i AS SELECT 'memory.main' AS place")
	cases := []struct {
		catalog, schema, want, healthCatalog, healthSchema string
	}{
		{"", "", "memory.main", "memory", "main"},
		{"live_other", "", "live_other.main", "live_other", "main"},
		{"", "reports", "memory.reports", "memory", "reports"},
		{"live_other", "reports", "live_other.reports", "live_other", "reports"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			ds := newLiveDatasource(t, server, map[string]any{"catalog": c.catalog, "schema": c.schema}, nil)
			// Many times: each query has a session of its own, and the order catalog, then schema must hold every time.
			for attempt := 0; attempt < 40; attempt++ {
				frame := mustQuery(t, ds, "SELECT place, current_database() AS c, current_schema() AS s FROM where_am_i", queryOptions{})
				if cell(frame.Fields[0], 0) != c.want {
					t.Fatalf("attempt %d: an unqualified table is %v, want %s", attempt, cell(frame.Fields[0], 0), c.want)
				}
			}
			health, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
			if err != nil || health.Status != backend.HealthStatusOk ||
				!strings.Contains(health.Message, "Catalog: "+c.healthCatalog+", schema: "+c.healthSchema+".") {
				t.Errorf("health: %v %v", health, err)
			}
		})
	}
	// A session's USE does not leak into the next query.
	ds := newLiveDatasource(t, server, nil, nil)
	mustExec(t, ds, "USE live_other.reports")
	if frame := mustQuery(t, ds, "SELECT place FROM where_am_i", queryOptions{}); cell(frame.Fields[0], 0) != "memory.main" {
		t.Errorf("after another query's USE: %v", cell(frame.Fields[0], 0))
	}
}

// Statements carry Grafana's context on the server.
func TestLiveQueryTag(t *testing.T) {
	server := plainServer(t)
	ds := newLiveDatasource(t, server, nil, nil)
	tagged, untagged := uniqueTag("live-tag"), uniqueTag("live-no-tag")
	mustQuery(t, ds, "SELECT 1 /* "+tagged+" */", queryOptions{User: "alice", Headers: map[string]string{
		"http_X-Dashboard-Uid": "dash-uid-1", "http_X-Panel-Id": "42", "http_X-Rule-Uid": "rule-7", "FromAlert": "true"}})
	status, _, tagText := statementStatus(t, ds, tagged)
	var tag map[string]any
	if err := json.Unmarshal([]byte(tagText), &tag); err != nil {
		t.Fatalf("the server's query_tag is %q (%v), status %q", tagText, err, status)
	}
	want := map[string]any{"app": "grafana", "plugin": PluginID, "datasource_uid": "live-test", "ref_id": "A", "namespace": "default",
		"user": "alice", "dashboard_uid": "dash-uid-1", "panel_id": "42", "rule_uid": "rule-7", "from_alert": true}
	for key, value := range want {
		if tag[key] != value {
			t.Errorf("the server's tag has %s = %v, want %v (tag %s)", key, tag[key], value, tagText)
		}
	}
	// Turned off: the server records no tag.
	silent := newLiveDatasource(t, server, map[string]any{"tagQueries": false}, nil)
	mustQuery(t, silent, "SELECT 1 /* "+untagged+" */", queryOptions{User: "alice"})
	if status, _, tagText := statementStatus(t, ds, untagged); status != "success" || tagText != "" {
		t.Errorf("with tagging off the server has status %q and tag %q", status, tagText)
	}
}

func signedInUser(t *testing.T, ds *Datasource) any {
	t.Helper()
	frame := mustQuery(t, ds, "SELECT HARDCHARGER_USER() AS u", queryOptions{})
	return cell(frame.Fields[0], 0)
}

// TLS: the CA certificate, the server name, skipping verification, a client
// certificate and signing in with it.
func TestLiveTLS(t *testing.T) {
	server := tlsServer(t)
	clientCert, clientKey, err := server.CA.issue(adminIdentity, false)
	if err != nil {
		t.Fatal(err)
	}
	otherCA, err := newTestCA()
	if err != nil {
		t.Fatal(err)
	}
	strangerCert, strangerKey, err := otherCA.issue(adminIdentity, false)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the CA certificate", func(t *testing.T) {
		ds := newLiveDatasource(t, server, nil, nil)
		if got := signedInUser(t, ds); got != testUsername {
			t.Errorf("signed in as %v", got)
		}
		frame := mustQuery(t, ds, "SELECT tls_enabled FROM hardcharger_server_info()", queryOptions{})
		if cell(frame.Fields[0], 0) != true {
			t.Errorf("the server says TLS is %v", cell(frame.Fields[0], 0))
		}
	})
	t.Run("without the CA certificate the server is not trusted", func(t *testing.T) {
		ds := newLiveDatasource(t, server, nil, map[string]string{"tlsCACert": ""})
		wantFailure(t, runQuery(t, ds, "SELECT 1", queryOptions{}), 502, backend.ErrorSourceDownstream, "x509", "certificate")
	})
	t.Run("another CA's certificate does not help", func(t *testing.T) {
		ds := newLiveDatasource(t, server, nil, map[string]string{"tlsCACert": otherCA.caPEM})
		wantFailure(t, runQuery(t, ds, "SELECT 1", queryOptions{}), 502, backend.ErrorSourceDownstream, "x509")
	})
	t.Run("skipping verification", func(t *testing.T) {
		ds := newLiveDatasource(t, server, map[string]any{"tlsSkipVerify": true}, map[string]string{"tlsCACert": ""})
		if got := signedInUser(t, ds); got != testUsername {
			t.Errorf("signed in as %v", got)
		}
	})
	t.Run("the server name", func(t *testing.T) {
		wrong := newLiveDatasource(t, server, map[string]any{"tlsServerName": "wrong.example"}, nil)
		wantFailure(t, runQuery(t, wrong, "SELECT 1", queryOptions{}), 502, backend.ErrorSourceDownstream, "x509", "wrong.example")
		right := newLiveDatasource(t, server, map[string]any{"tlsServerName": "hardcharger.test"}, nil)
		if got := signedInUser(t, right); got != testUsername {
			t.Errorf("signed in as %v", got)
		}
	})
	t.Run("a connection URI", func(t *testing.T) {
		ds := newLiveDatasource(t, server, map[string]any{"uri": fmt.Sprintf("hardcharger://127.0.0.1:%d", server.Port),
			"host": "ignored.invalid", "port": 1, "useTLS": false}, nil)
		if got := signedInUser(t, ds); got != testUsername {
			t.Errorf("signed in as %v", got)
		}
	})
	t.Run("a client certificate with a password", func(t *testing.T) {
		ds := newLiveDatasource(t, server, nil, map[string]string{"tlsClientCert": clientCert, "tlsClientKey": clientKey})
		if got := signedInUser(t, ds); got != testUsername {
			t.Errorf("signed in as %v", got)
		}
	})
	t.Run("signing in with the client certificate", func(t *testing.T) {
		ds := newLiveDatasource(t, server, map[string]any{"authType": "client_certificate", "username": ""},
			map[string]string{"password": "", "tlsClientCert": clientCert, "tlsClientKey": clientKey})
		if got := signedInUser(t, ds); got != adminIdentity {
			t.Errorf("signed in as %v, want %s", got, adminIdentity)
		}
		health, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
		if err != nil || health.Status != backend.HealthStatusOk {
			t.Errorf("health: %v %v", health, err)
		}
	})
	t.Run("a client certificate of another CA is refused", func(t *testing.T) {
		ds := newLiveDatasource(t, server, map[string]any{"authType": "client_certificate", "username": ""},
			map[string]string{"password": "", "tlsClientCert": strangerCert, "tlsClientKey": strangerKey})
		result := runQuery(t, ds, "SELECT 1", queryOptions{})
		if result.Error == nil || result.ErrorSource != backend.ErrorSourceDownstream || (result.Status != 502 && result.Status != 401) {
			t.Fatalf("got %d %s %v", result.Status, result.ErrorSource, result.Error)
		}
		if strings.Contains(result.Error.Error(), "PRIVATE KEY") {
			t.Errorf("the error repeats the key: %v", result.Error)
		}
	})
	t.Run("a client certificate that is not PEM", func(t *testing.T) {
		ds := newLiveDatasource(t, server, nil, map[string]string{"tlsClientCert": "not a certificate", "tlsClientKey": "not-a-key-s3cret"})
		result := runQuery(t, ds, "SELECT 1", queryOptions{})
		if result.Error == nil {
			t.Fatal("the query succeeded")
		}
		if strings.Contains(result.Error.Error(), "not-a-key-s3cret") {
			t.Errorf("the error repeats the key: %v", result.Error)
		}
	})
}

// An access token instead of a username and password.
func TestLiveAccessToken(t *testing.T) {
	server := plainServer(t)
	// A token the server issued: the one the driver got by signing in with the password.
	ctx := context.Background()
	db, err := hardcharger.NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(ctx, map[string]string{
		adbc.OptionKeyURI: fmt.Sprintf("hardcharger://127.0.0.1:%d?tls=false", server.Port), adbc.OptionKeyUsername: testUsername,
		adbc.OptionKeyPassword: server.Password})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	cnxn, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cnxn.Close(ctx) }()
	token, err := db.(adbc.GetSetOptionsWithContext).GetOption(ctx, "adbc.chargewire.auth.access_token")
	if err != nil || token == "" {
		t.Fatalf("no access token from the driver: %q, %v", token, err)
	}

	ds := newLiveDatasource(t, server, map[string]any{"authType": "token", "username": ""}, map[string]string{"password": "", "token": token})
	if got := signedInUser(t, ds); got != testUsername {
		t.Errorf("signed in as %v", got)
	}
	// The negative twin: a token the server never issued is refused, and not repeated.
	forged := newLiveDatasource(t, server, map[string]any{"authType": "token", "username": ""},
		map[string]string{"password": "", "token": token[:len(token)-6] + "AAAAAA"})
	result := runQuery(t, forged, "SELECT 1", queryOptions{})
	wantFailure(t, result, 401, backend.ErrorSourceDownstream)
	if strings.Contains(result.Error.Error(), token[:20]) {
		t.Errorf("the error repeats the token: %v", result.Error)
	}
}

// Many queries at once, in one request and across requests, each in its own
// session.
func TestLiveConcurrentQueries(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	request := &backend.QueryDataRequest{PluginContext: backend.PluginContext{Namespace: "default"}}
	for index := 0; index < 12; index++ {
		model, _ := json.Marshal(map[string]any{"rawSql": fmt.Sprintf("SELECT %d AS n, HARDCHARGER_CURRENT_SESSION() AS session", index)})
		request.Queries = append(request.Queries, backend.DataQuery{RefID: fmt.Sprintf("Q%d", index), JSON: model,
			TimeRange: backend.TimeRange{From: time.Now().Add(-time.Hour), To: time.Now()}})
	}
	var wg sync.WaitGroup
	sessions := sync.Map{}
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := ds.QueryData(context.Background(), request)
			if err != nil {
				t.Error(err)
				return
			}
			if len(response.Responses) != 12 {
				t.Errorf("%d responses", len(response.Responses))
			}
			for index := 0; index < 12; index++ {
				result := response.Responses[fmt.Sprintf("Q%d", index)]
				if result.Error != nil {
					t.Errorf("query %d: %v", index, result.Error)
					continue
				}
				frame := result.Frames[0]
				if cell(frame.Fields[0], 0) != int32(index) || frame.RefID != fmt.Sprintf("Q%d", index) {
					t.Errorf("query %d returned %v as %s", index, cell(frame.Fields[0], 0), frame.RefID)
				}
				sessions.Store(cell(frame.Fields[1], 0), true)
			}
		}()
	}
	wg.Wait()
	count := 0
	sessions.Range(func(_, _ any) bool { count++; return true })
	if count != 60 {
		t.Errorf("60 queries ran in %d sessions", count)
	}
	// And the sessions are gone again: the plugin closes each one.
	deadline := time.Now().Add(scaled(10 * time.Second))
	for {
		frame := mustQuery(t, ds, "SELECT count(*) FROM hardcharger_sessions() WHERE NOT is_current", queryOptions{})
		if cell(frame.Fields[0], 0) == int64(0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v sessions are still open on the server", cell(frame.Fields[0], 0))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Dispose (new settings were saved) lets a query that is running finish, and
// closes the handle after it.
func TestLiveDisposeWaitsForRunningQueries(t *testing.T) {
	tag := uniqueTag("live-dispose")
	server := plainServer(t)
	admin := newLiveDatasource(t, server, nil, nil)
	ds := newLiveDatasource(t, server, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // whatever happens below, the endless query ends
	finished := make(chan backend.DataResponse, 1)
	go func() { finished <- runQuery(t, ds, endlessQuery(tag), queryOptions{Context: ctx}) }()
	waitForStatus(t, admin, tag, "executing")

	disposed := make(chan time.Time, 1)
	go func() { ds.Dispose(); disposed <- time.Now() }()
	select {
	case <-disposed:
		t.Fatal("Dispose returned while a query was running")
	case <-time.After(scaled(1500 * time.Millisecond)):
	}
	if status, _, _ := statementStatus(t, admin, tag); status != "executing" {
		t.Fatalf("while Dispose waits the statement is %q", status)
	}
	cancel() // the query ends; Dispose may go on
	var ended time.Time
	select {
	case <-finished:
		ended = time.Now()
	case <-time.After(scaled(15 * time.Second)):
		t.Fatal("the query did not end")
	}
	select {
	case at := <-disposed:
		if at.Before(ended.Add(-50 * time.Millisecond)) {
			t.Errorf("Dispose returned %s before the query ended", ended.Sub(at))
		}
	case <-time.After(scaled(15 * time.Second)):
		t.Fatal("Dispose did not return after the query ended")
	}
	wantFailure(t, runQuery(t, ds, "SELECT 1", queryOptions{}), 500, backend.ErrorSourcePlugin, "was replaced by newer settings")
}
