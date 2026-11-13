package plugin

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Macros are expanded on the SQL's tokens, never on its raw text: a macro
// name inside a string literal, a quoted identifier, a dollar-quoted string
// or a comment is left alone.
//
//	$__timeFilter(col)                    col >= '<from>' AND col <= '<to>'
//	$__timeFrom()  $__timeTo()            '<from>'  '<to>'  (RFC 3339, UTC)
//	$__timeGroup(col, interval[, fill])   time_bucket(INTERVAL '<n> seconds', col)
//	$__timeGroupAlias(col, interval[, fill])   the same, AS "time"
//	$__unixEpochFilter(col)               col >= <from> AND col <= <to>  (seconds)
//	$__unixEpochFrom()  $__unixEpochTo()  <from>  <to>  (seconds)
//	$__unixEpochGroup(col, interval[, fill])   CAST(floor((col) / <n>) * <n> AS BIGINT)
//	$__unixEpochGroupAlias(col, interval[, fill])   the same, AS "time"
//	$__interval  $__interval_ms           the panel's interval: 30s, 30000
//	$__from  $__to                        the range in epoch milliseconds
//
// $__timeFrom, $__timeTo and $__timeFilter also work without parentheses, as
// in the GizmoSQL data source ($__timeFilter then filters a column named
// time).

// macroParams is what a query's macros expand from.
type macroParams struct {
	From     time.Time
	To       time.Time
	Interval time.Duration
}

// fillSpec is the gap filling a $__timeGroup or $__unixEpochGroup asked for.
type fillSpec struct {
	Interval time.Duration
	Fill     data.FillMissing
	// Origin is the instant the server's buckets are aligned to.
	Origin time.Time
}

// macroError is an error in the query's macros: the user's to fix.
type macroError struct{ msg string }

func (e *macroError) Error() string { return e.msg }

func macroErrorf(format string, args ...any) error {
	return &macroError{msg: fmt.Sprintf(format, args...)}
}

// timeBucketOrigin is the origin of DuckDB's time_bucket for widths without
// months: Monday 2000-01-03 00:00:00 UTC.
var timeBucketOrigin = time.Date(2000, 1, 3, 0, 0, 0, 0, time.UTC)

const timeLiteralLayout = "2006-01-02T15:04:05.000Z"

var supportedMacros = "$__timeFilter(column), $__timeFrom(), $__timeTo(), $__timeGroup(column, interval[, fill]), " +
	"$__timeGroupAlias(column, interval[, fill]), $__unixEpochFilter(column), $__unixEpochFrom(), " +
	"$__unixEpochTo(), $__unixEpochGroup(column, interval[, fill]), $__unixEpochGroupAlias(column, interval[, fill]), " +
	"$__interval, $__interval_ms, $__from, $__to"

// expandMacros returns sql with its macros expanded, and the gap filling its
// time grouping asked for (nil: none).
func expandMacros(sql string, params macroParams) (string, *fillSpec, error) {
	expander := &macroExpander{params: params}
	out, err := expander.expand(sql, 0)
	if err != nil {
		return "", nil, err
	}
	return out, expander.fill, nil
}

type macroExpander struct {
	params macroParams
	fill   *fillSpec
}

const maxMacroDepth = 8

func (m *macroExpander) expand(sql string, depth int) (string, error) {
	if depth > maxMacroDepth {
		return "", macroErrorf("macros are nested too deeply")
	}
	var out strings.Builder
	out.Grow(len(sql) + 64)
	i := 0
	for i < len(sql) {
		if end := skipNonCode(sql, i); end > i {
			out.WriteString(sql[i:end])
			i = end
			continue
		}
		if sql[i] != '$' {
			out.WriteByte(sql[i])
			i++
			continue
		}
		name, braces, end := macroNameAt(sql, i)
		if name == "" {
			out.WriteByte(sql[i])
			i++
			continue
		}
		var args []string
		hasArgs := false
		if !braces && end < len(sql) && sql[end] == '(' {
			closing, parsed, ok := macroArguments(sql, end)
			if !ok {
				return "", macroErrorf("$__%s( has no closing parenthesis", name)
			}
			for index, arg := range parsed {
				expanded, err := m.expand(arg, depth+1)
				if err != nil {
					return "", err
				}
				parsed[index] = strings.TrimSpace(expanded)
			}
			args, hasArgs, end = parsed, true, closing+1
		}
		replacement, err := m.macro(name, args, hasArgs)
		if err != nil {
			return "", err
		}
		out.WriteString(replacement)
		i = end
	}
	return out.String(), nil
}

// skipNonCode returns the end of the string literal, quoted identifier,
// dollar-quoted string or comment that starts at i, or i when none does.
func skipNonCode(sql string, i int) int {
	switch c := sql[i]; {
	case c == '\'':
		// e'...' and E'...' strings take backslash escapes.
		escapes := i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isIdentifierByte(sql[i-2]))
		return skipQuoted(sql, i, '\'', escapes)
	case c == '"':
		return skipQuoted(sql, i, '"', false)
	case c == '-' && strings.HasPrefix(sql[i:], "--"):
		if newline := strings.IndexByte(sql[i:], '\n'); newline >= 0 {
			return i + newline + 1
		}
		return len(sql)
	case c == '/' && strings.HasPrefix(sql[i:], "/*"):
		// Block comments nest, as in PostgreSQL and DuckDB.
		depth := 0
		for j := i; j < len(sql); j++ {
			switch {
			case strings.HasPrefix(sql[j:], "/*"):
				depth++
				j++
			case strings.HasPrefix(sql[j:], "*/"):
				depth--
				j++
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(sql)
	case c == '$':
		// $$ ... $$ and $tag$ ... $tag$.
		j := i + 1
		for j < len(sql) && isIdentifierByte(sql[j]) {
			j++
		}
		if j >= len(sql) || sql[j] != '$' || (j > i+1 && isDigit(sql[i+1])) {
			return i
		}
		tag := sql[i : j+1]
		if closing := strings.Index(sql[j+1:], tag); closing >= 0 {
			return j + 1 + closing + len(tag)
		}
		return len(sql)
	}
	return i
}

func skipQuoted(sql string, i int, quote byte, backslashEscapes bool) int {
	for j := i + 1; j < len(sql); j++ {
		switch {
		case backslashEscapes && sql[j] == '\\':
			j++
		case sql[j] == quote:
			if j+1 < len(sql) && sql[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(sql)
}

func isIdentifierByte(c byte) bool {
	return c == '_' || isDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// macroNameAt reads the macro that starts at sql[i] ('$'): $__name or
// ${__name}. It returns "" when there is none.
func macroNameAt(sql string, i int) (name string, braces bool, end int) {
	j := i + 1
	if j < len(sql) && sql[j] == '{' {
		braces = true
		j++
	}
	if !strings.HasPrefix(sql[j:], "__") {
		return "", false, i
	}
	start := j + 2
	j = start
	for j < len(sql) && isIdentifierByte(sql[j]) {
		j++
	}
	if j == start {
		return "", false, i
	}
	name = sql[start:j]
	if braces {
		if j >= len(sql) || sql[j] != '}' {
			return "", false, i // ${__name:format} and the like are Grafana's, not ours
		}
		j++
	}
	return name, braces, j
}

// macroArguments splits the parenthesised arguments that open at sql[open]
// on their top-level commas. It returns the index of the closing parenthesis.
func macroArguments(sql string, open int) (closing int, args []string, ok bool) {
	depth := 0
	start := open + 1
	for j := open; j < len(sql); {
		if end := skipNonCode(sql, j); end > j {
			j = end
			continue
		}
		switch sql[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if last := sql[start:j]; len(args) > 0 || strings.TrimSpace(last) != "" {
					args = append(args, last)
				}
				return j, args, true
			}
		case ',':
			if depth == 1 {
				args = append(args, sql[start:j])
				start = j + 1
			}
		}
		j++
	}
	return 0, nil, false
}

func (m *macroExpander) macro(name string, args []string, hasArgs bool) (string, error) {
	from, to := m.params.From.UTC(), m.params.To.UTC()
	wantArgs := func(least, most int, usage string) error {
		if len(args) < least || len(args) > most {
			return macroErrorf("$__%s takes %s, got %d argument(s)", name, usage, len(args))
		}
		for _, arg := range args {
			if arg == "" {
				return macroErrorf("$__%s has an empty argument", name)
			}
		}
		return nil
	}
	switch name {
	case "timeFilter":
		column := `"time"` // the GizmoSQL data source's bare $__timeFilter
		if hasArgs {
			if err := wantArgs(1, 1, "one argument, the time column"); err != nil {
				return "", err
			}
			column = args[0]
		}
		return fmt.Sprintf("%s >= %s AND %s <= %s", column, timeLiteral(from), column, timeLiteral(to)), nil
	case "timeFrom":
		if err := wantArgs(0, 0, "no arguments"); err != nil {
			return "", err
		}
		return timeLiteral(from), nil
	case "timeTo":
		if err := wantArgs(0, 0, "no arguments"); err != nil {
			return "", err
		}
		return timeLiteral(to), nil
	case "timeGroup", "timeGroupAlias":
		if err := wantArgs(2, 3, "a time column, an interval and an optional fill"); err != nil {
			return "", err
		}
		interval, err := parseGroupInterval(args[1])
		if err != nil {
			return "", macroErrorf("$__%s: %s", name, err)
		}
		if len(args) == 3 {
			if interval.duration == 0 {
				return "", macroErrorf("$__%s: filling gaps needs a fixed-length interval (ms, s, m, h, d or w), not %s",
					name, args[1])
			}
			if err := m.setFill(name, args[2], interval.duration, timeBucketOrigin); err != nil {
				return "", err
			}
		}
		expr := fmt.Sprintf("time_bucket(INTERVAL '%s', %s)", interval.sql, args[0])
		if name == "timeGroupAlias" {
			expr += ` AS "time"`
		}
		return expr, nil
	case "unixEpochFilter":
		if err := wantArgs(1, 1, "one argument, the column holding Unix seconds"); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s >= %d AND %s <= %d", args[0], from.Unix(), args[0], to.Unix()), nil
	case "unixEpochFrom":
		if err := wantArgs(0, 0, "no arguments"); err != nil {
			return "", err
		}
		return strconv.FormatInt(from.Unix(), 10), nil
	case "unixEpochTo":
		if err := wantArgs(0, 0, "no arguments"); err != nil {
			return "", err
		}
		return strconv.FormatInt(to.Unix(), 10), nil
	case "unixEpochGroup", "unixEpochGroupAlias":
		if err := wantArgs(2, 3, "a column holding Unix seconds, an interval and an optional fill"); err != nil {
			return "", err
		}
		interval, err := parseGroupInterval(args[1])
		if err != nil {
			return "", macroErrorf("$__%s: %s", name, err)
		}
		seconds := int64(interval.duration / time.Second)
		if interval.duration == 0 || interval.duration%time.Second != 0 {
			return "", macroErrorf("$__%s: the interval must be a whole number of seconds, minutes, hours, days "+
				"or weeks, not %s", name, args[1])
		}
		if len(args) == 3 {
			if err := m.setFill(name, args[2], interval.duration, time.Unix(0, 0).UTC()); err != nil {
				return "", err
			}
		}
		expr := fmt.Sprintf("CAST(floor((%s) / %d) * %d AS BIGINT)", args[0], seconds, seconds)
		if name == "unixEpochGroupAlias" {
			expr += ` AS "time"`
		}
		return expr, nil
	case "interval":
		if hasArgs {
			return "", macroErrorf("$__interval takes no arguments")
		}
		return formatInterval(m.params.Interval), nil
	case "interval_ms":
		if hasArgs {
			return "", macroErrorf("$__interval_ms takes no arguments")
		}
		return strconv.FormatInt(max(m.params.Interval.Milliseconds(), 1), 10), nil
	case "from":
		if hasArgs {
			return "", macroErrorf("$__from takes no arguments")
		}
		return strconv.FormatInt(from.UnixMilli(), 10), nil
	case "to":
		if hasArgs {
			return "", macroErrorf("$__to takes no arguments")
		}
		return strconv.FormatInt(to.UnixMilli(), 10), nil
	}
	return "", macroErrorf("unknown macro $__%s. The macros are: %s", name, supportedMacros)
}

func timeLiteral(t time.Time) string {
	return "'" + t.UTC().Format(timeLiteralLayout) + "'"
}

func (m *macroExpander) setFill(macro, arg string, interval time.Duration, origin time.Time) error {
	fill := data.FillMissing{}
	switch strings.ToLower(arg) {
	case "null":
		fill.Mode = data.FillModeNull
	case "previous":
		fill.Mode = data.FillModePrevious
	default:
		value, err := strconv.ParseFloat(arg, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return macroErrorf("$__%s: the fill must be NULL, previous or a number, not %s", macro, arg)
		}
		fill.Mode = data.FillModeValue
		fill.Value = value
	}
	m.fill = &fillSpec{Interval: interval, Fill: fill, Origin: origin}
	return nil
}

// groupInterval is a time grouping's bucket width: as a DuckDB interval, and
// as a duration when its length is fixed (0 for months and years).
type groupInterval struct {
	sql      string
	duration time.Duration
}

var intervalPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(ms|s|m|h|d|w|M|y)$`)

// parseGroupInterval reads a Grafana interval (30s, 5m, 1h, 1d, 1w, 1M, 1y),
// with or without quotes around it.
func parseGroupInterval(arg string) (groupInterval, error) {
	text := strings.TrimSpace(arg)
	if len(text) >= 2 && (text[0] == '\'' || text[0] == '"') && text[len(text)-1] == text[0] {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	match := intervalPattern.FindStringSubmatch(text)
	if match == nil {
		return groupInterval{}, fmt.Errorf("the interval must look like 30s, 5m, 1h, 1d, 1w, 1M or 1y "+
			"(or $__interval), not %s", arg)
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil || amount <= 0 {
		return groupInterval{}, fmt.Errorf("the interval must be above zero, not %s", arg)
	}
	unit := match[2]
	if unit == "M" || unit == "y" {
		if amount != math.Trunc(amount) {
			return groupInterval{}, fmt.Errorf("an interval in months or years must be a whole number, not %s", arg)
		}
		name := map[string]string{"M": "months", "y": "years"}[unit]
		return groupInterval{sql: fmt.Sprintf("%d %s", int64(amount), name)}, nil
	}
	unitLength := map[string]time.Duration{
		"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
		"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
	}[unit]
	duration := time.Duration(math.Round(amount * float64(unitLength)))
	if duration < time.Millisecond {
		return groupInterval{}, fmt.Errorf("the interval must be at least 1ms, not %s", arg)
	}
	duration = duration.Round(time.Millisecond)
	// Always in seconds or milliseconds, never days: DuckDB then buckets by
	// elapsed time from a UTC origin, whatever the session's time zone.
	if duration%time.Second == 0 {
		return groupInterval{sql: fmt.Sprintf("%d seconds", int64(duration/time.Second)), duration: duration}, nil
	}
	return groupInterval{sql: fmt.Sprintf("%d milliseconds", duration.Milliseconds()), duration: duration}, nil
}

// formatInterval writes a duration as Grafana does: 500ms, 30s, 5m, 2h, 1d.
func formatInterval(interval time.Duration) string {
	if interval <= 0 {
		interval = time.Millisecond
	}
	switch {
	case interval%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", interval/(24*time.Hour))
	case interval%time.Hour == 0:
		return fmt.Sprintf("%dh", interval/time.Hour)
	case interval%time.Minute == 0:
		return fmt.Sprintf("%dm", interval/time.Minute)
	case interval%time.Second == 0:
		return fmt.Sprintf("%ds", interval/time.Second)
	}
	return fmt.Sprintf("%dms", max(interval.Milliseconds(), 1))
}
