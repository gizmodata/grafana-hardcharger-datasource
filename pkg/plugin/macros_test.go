package plugin

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

var testParams = macroParams{
	From:     time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC),
	To:       time.Date(2026, 11, 13, 11, 30, 0, 250_000_000, time.UTC),
	Interval: 30 * time.Second,
}

const (
	fromLiteral = "'2026-11-13T10:00:00.000Z'"
	toLiteral   = "'2026-11-13T11:30:00.250Z'"
	fromEpoch   = "1794564000"
	toEpoch     = "1794569400"
)

func TestExpandMacros(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"no macros", "SELECT 1", "SELECT 1"},
		{"timeFilter", "SELECT * FROM t WHERE $__timeFilter(ts)",
			"SELECT * FROM t WHERE ts >= " + fromLiteral + " AND ts <= " + toLiteral},
		{"timeFilter of an expression with commas, parentheses and a string",
			"WHERE $__timeFilter(coalesce(a, strptime(b, '%Y,%m)')))",
			"WHERE coalesce(a, strptime(b, '%Y,%m)')) >= " + fromLiteral + " AND coalesce(a, strptime(b, '%Y,%m)')) <= " + toLiteral},
		{"timeFilter without parentheses filters the column named time", "WHERE $__timeFilter",
			`WHERE "time" >= ` + fromLiteral + ` AND "time" <= ` + toLiteral},
		{"timeFrom and timeTo", "WHERE ts BETWEEN $__timeFrom() AND $__timeTo()",
			"WHERE ts BETWEEN " + fromLiteral + " AND " + toLiteral},
		{"timeFrom and timeTo without parentheses", "WHERE ts >= $__timeFrom AND ts <= $__timeTo",
			"WHERE ts >= " + fromLiteral + " AND ts <= " + toLiteral},
		{"a bare macro at the end and before punctuation", "SELECT $__timeFrom, $__timeTo",
			"SELECT " + fromLiteral + ", " + toLiteral},
		{"timeGroup", "SELECT $__timeGroup(ts, '5m') AS time", "SELECT time_bucket(INTERVAL '300 seconds', ts) AS time"},
		{"timeGroup with an unquoted interval", "$__timeGroup(ts, 1h)", "time_bucket(INTERVAL '3600 seconds', ts)"},
		{"timeGroup with $__interval", "$__timeGroup(ts, $__interval)", "time_bucket(INTERVAL '30 seconds', ts)"},
		{"timeGroup with the interval Grafana's frontend filled in", "$__timeGroup(ts, '30s')",
			"time_bucket(INTERVAL '30 seconds', ts)"},
		{"timeGroup in milliseconds", "$__timeGroup(ts, 500ms)", "time_bucket(INTERVAL '500 milliseconds', ts)"},
		{"timeGroup in days is seconds, so buckets are UTC days", "$__timeGroup(ts, 1d)",
			"time_bucket(INTERVAL '86400 seconds', ts)"},
		{"timeGroup in weeks", "$__timeGroup(ts, 1w)", "time_bucket(INTERVAL '604800 seconds', ts)"},
		{"timeGroup in months", "$__timeGroup(ts, 1M)", "time_bucket(INTERVAL '1 months', ts)"},
		{"timeGroup in years", "$__timeGroup(ts, 2y)", "time_bucket(INTERVAL '2 years', ts)"},
		{"timeGroup with a fill", "$__timeGroup(ts, 5m, NULL)", "time_bucket(INTERVAL '300 seconds', ts)"},
		{"timeGroupAlias", "SELECT $__timeGroupAlias(ts, 5m), avg(v)",
			`SELECT time_bucket(INTERVAL '300 seconds', ts) AS "time", avg(v)`},
		{"unixEpochFilter", "WHERE $__unixEpochFilter(epoch)",
			"WHERE epoch >= " + fromEpoch + " AND epoch <= " + toEpoch},
		{"unixEpochFrom and unixEpochTo", "$__unixEpochFrom() $__unixEpochTo()", fromEpoch + " " + toEpoch},
		{"unixEpochGroup", "$__unixEpochGroup(epoch, 5m)", "CAST(floor((epoch) / 300) * 300 AS BIGINT)"},
		{"unixEpochGroupAlias", "$__unixEpochGroupAlias(epoch, 1h, 0)", `CAST(floor((epoch) / 3600) * 3600 AS BIGINT) AS "time"`},
		{"interval", "SELECT '$__interval', $__interval_ms", "SELECT '$__interval', 30000"},
		{"interval outside a string", "$__interval $__interval_ms", "30s 30000"},
		{"interval in braces", "${__interval} ${__interval_ms}", "30s 30000"},
		{"from and to in milliseconds", "$__from $__to", "1794564000000 1794569400250"},
		{"a macro in a string literal stays", "SELECT '$__timeFrom()' AS s, $__timeFrom()",
			"SELECT '$__timeFrom()' AS s, " + fromLiteral},
		{"a string with a doubled quote", "SELECT 'it''s $__timeTo()', $__timeTo()", "SELECT 'it''s $__timeTo()', " + toLiteral},
		{"an escape string with a backslashed quote", `SELECT e'a\'$__timeTo()', $__timeTo()`,
			`SELECT e'a\'$__timeTo()', ` + toLiteral},
		{"a plain string ends at a backslash-quote", `SELECT 'a\', $__timeTo()`, `SELECT 'a\', ` + toLiteral},
		{"a macro in a quoted identifier stays", `SELECT "$__timeFrom()" FROM t WHERE $__timeFilter(ts)`,
			`SELECT "$__timeFrom()" FROM t WHERE ts >= ` + fromLiteral + " AND ts <= " + toLiteral},
		{"a macro in a line comment stays", "SELECT 1 -- $__timeFilter(ts)\nWHERE $__timeFilter(ts)",
			"SELECT 1 -- $__timeFilter(ts)\nWHERE ts >= " + fromLiteral + " AND ts <= " + toLiteral},
		{"a macro in a block comment stays", "SELECT /* $__timeFrom() */ $__timeFrom()", "SELECT /* $__timeFrom() */ " + fromLiteral},
		{"block comments nest", "SELECT /* a /* $__nope() */ $__timeFrom() */ $__timeFrom()",
			"SELECT /* a /* $__nope() */ $__timeFrom() */ " + fromLiteral},
		{"a macro in a dollar-quoted string stays", "SELECT $$ $__timeFrom() $$, $tag$ $__timeTo() $tag$, $__timeTo()",
			"SELECT $$ $__timeFrom() $$, $tag$ $__timeTo() $tag$, " + toLiteral},
		{"positional parameters are not dollar quotes", "SELECT $1, $2 WHERE $__timeFilter(ts)",
			"SELECT $1, $2 WHERE ts >= " + fromLiteral + " AND ts <= " + toLiteral},
		{"Grafana's own formats are not ours", "SELECT '${__from:date:iso}'", "SELECT '${__from:date:iso}'"},
		{"an unterminated string keeps the rest as it is", "SELECT 'abc $__timeFrom()", "SELECT 'abc $__timeFrom()"},
		{"text that is not ASCII is kept", "SELECT 'héllo ✓' AS \"naïve\", $__timeFrom()",
			"SELECT 'héllo ✓' AS \"naïve\", " + fromLiteral},
		{"a lone dollar", "SELECT '$' || x, 5 $ 3", "SELECT '$' || x, 5 $ 3"},
		{"a comment inside a macro's argument", "$__timeFilter(ts /* ) */)", "ts /* ) */ >= " + fromLiteral + " AND ts /* ) */ <= " + toLiteral},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := expandMacros(c.sql, testParams)
			if err != nil {
				t.Fatalf("expandMacros(%q): %v", c.sql, err)
			}
			if got != c.want {
				t.Errorf("expandMacros(%q)\n got: %s\nwant: %s", c.sql, got, c.want)
			}
		})
	}
}

func TestExpandMacrosErrors(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT $__timeFilte(ts)", "unknown macro $__timeFilte"},
		{"SELECT $__nope", "unknown macro $__nope"},
		{"WHERE $__timeFilter(ts", "$__timeFilter( has no closing parenthesis"},
		{"WHERE $__timeFilter()", "$__timeFilter takes one argument, the time column, got 0 argument(s)"},
		{"WHERE $__timeFilter(a, b)", "$__timeFilter takes one argument, the time column, got 2 argument(s)"},
		{"WHERE $__timeFrom(ts)", "$__timeFrom takes no arguments, got 1 argument(s)"},
		{"$__timeGroup(ts)", "$__timeGroup takes a time column, an interval and an optional fill, got 1 argument(s)"},
		{"$__timeGroup(ts, )", "$__timeGroup has an empty argument"},
		{"$__timeGroup(ts, 5 parsecs)", "the interval must look like 30s, 5m, 1h, 1d, 1w, 1M or 1y"},
		{"$__timeGroup(ts, 0m)", "the interval must be above zero"},
		{"$__timeGroup(ts, 5m, sideways)", "the fill must be NULL, previous or a number, not sideways"},
		{"$__timeGroup(ts, 1M, NULL)", "filling gaps needs a fixed-length interval"},
		{"$__unixEpochGroup(e, 500ms)", "the interval must be a whole number of seconds"},
		{"$__unixEpochGroup(e, 1M)", "the interval must be a whole number of seconds"},
		{"$__interval(1)", "$__interval takes no arguments"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			_, _, err := expandMacros(c.sql, testParams)
			var macroErr *macroError
			if !errors.As(err, &macroErr) {
				t.Fatalf("expandMacros(%q) = %v, want a macro error", c.sql, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("expandMacros(%q) error %q does not say %q", c.sql, err, c.want)
			}
		})
	}
}

func TestExpandMacrosFill(t *testing.T) {
	cases := []struct {
		sql      string
		mode     data.FillMode
		value    float64
		interval time.Duration
		origin   time.Time
	}{
		{"$__timeGroup(ts, 5m, NULL)", data.FillModeNull, 0, 5 * time.Minute, timeBucketOrigin},
		{"$__timeGroup(ts, 5m, null)", data.FillModeNull, 0, 5 * time.Minute, timeBucketOrigin},
		{"$__timeGroup(ts, 1h, previous)", data.FillModePrevious, 0, time.Hour, timeBucketOrigin},
		{"$__timeGroupAlias(ts, 10s, 0)", data.FillModeValue, 0, 10 * time.Second, timeBucketOrigin},
		{"$__timeGroup(ts, 10s, -1.5)", data.FillModeValue, -1.5, 10 * time.Second, timeBucketOrigin},
		{"$__unixEpochGroup(e, 1m, 7)", data.FillModeValue, 7, time.Minute, time.Unix(0, 0).UTC()},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			_, fill, err := expandMacros(c.sql, testParams)
			if err != nil {
				t.Fatal(err)
			}
			if fill == nil {
				t.Fatal("no fill")
			}
			if fill.Fill.Mode != c.mode || fill.Fill.Value != c.value || fill.Interval != c.interval || !fill.Origin.Equal(c.origin) {
				t.Errorf("got %+v", *fill)
			}
		})
	}
	if _, fill, err := expandMacros("$__timeGroup(ts, 5m)", testParams); err != nil || fill != nil {
		t.Errorf("without a fill argument: fill %v, err %v", fill, err)
	}
	if _, fill, err := expandMacros("SELECT '$__timeGroup(ts, 5m, NULL)'", testParams); err != nil || fill != nil {
		t.Errorf("a macro in a string must not set a fill: fill %v, err %v", fill, err)
	}
}

func TestFormatInterval(t *testing.T) {
	for duration, want := range map[time.Duration]string{
		0:                       "1ms",
		500 * time.Millisecond:  "500ms",
		1500 * time.Millisecond: "1500ms",
		30 * time.Second:        "30s",
		90 * time.Second:        "90s",
		5 * time.Minute:         "5m",
		2 * time.Hour:           "2h",
		48 * time.Hour:          "2d",
	} {
		if got := formatInterval(duration); got != want {
			t.Errorf("formatInterval(%s) = %q, want %q", duration, got, want)
		}
		// What $__interval gives must be an interval $__timeGroup reads.
		if _, err := parseGroupInterval(formatInterval(duration)); err != nil {
			t.Errorf("parseGroupInterval(formatInterval(%s)): %v", duration, err)
		}
	}
}
