package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// typeCase is one DuckDB type: a value of it, and the Grafana field and
// value it must become.
type typeCase struct {
	name      string // also the column's type, unless column is set
	column    string
	value     string // a SQL expression
	fieldType data.FieldType
	want      any
}

func (c typeCase) columnType() string {
	if c.column != "" {
		return c.column
	}
	return c.name
}

func utc(year int, month time.Month, day, hour, minute, second, nanos int) time.Time {
	return time.Date(year, month, day, hour, minute, second, nanos, time.UTC)
}

// duckDBTypes is every DuckDB type, as the server sends it by default.
var duckDBTypes = []typeCase{
	{name: "BOOLEAN", value: "true", fieldType: data.FieldTypeNullableBool, want: true},
	{name: "TINYINT", value: "-128", fieldType: data.FieldTypeNullableInt8, want: int8(-128)},
	{name: "SMALLINT", value: "-32768", fieldType: data.FieldTypeNullableInt16, want: int16(-32768)},
	{name: "INTEGER", value: "-2147483648", fieldType: data.FieldTypeNullableInt32, want: int32(math.MinInt32)},
	{name: "BIGINT", value: "-9223372036854775807 - 1", fieldType: data.FieldTypeNullableInt64, want: int64(math.MinInt64)},
	{name: "HUGEINT", value: "-1267650600228229401496703205376", fieldType: data.FieldTypeNullableFloat64, want: -math.Pow(2, 100)},
	{name: "UTINYINT", value: "255", fieldType: data.FieldTypeNullableUint8, want: uint8(255)},
	{name: "USMALLINT", value: "65535", fieldType: data.FieldTypeNullableUint16, want: uint16(65535)},
	{name: "UINTEGER", value: "4294967295", fieldType: data.FieldTypeNullableUint32, want: uint32(math.MaxUint32)},
	{name: "UBIGINT", value: "18446744073709551615", fieldType: data.FieldTypeNullableUint64, want: uint64(math.MaxUint64)},
	{name: "UHUGEINT", value: "1267650600228229401496703205376", fieldType: data.FieldTypeNullableFloat64, want: math.Pow(2, 100)},
	{name: "DECIMAL(4,1)", value: "123.4", fieldType: data.FieldTypeNullableFloat64, want: 123.4},
	{name: "DECIMAL(9,2)", value: "-1234567.89", fieldType: data.FieldTypeNullableFloat64, want: -1234567.89},
	{name: "DECIMAL(18,3)", value: "123456789012345.678", fieldType: data.FieldTypeNullableFloat64, want: 123456789012345.678},
	{name: "DECIMAL(38,10)", value: "1234567890123456789012345678.0123456789", fieldType: data.FieldTypeNullableFloat64,
		want: 1234567890123456789012345678.0123456789},
	{name: "FLOAT", value: "1.5", fieldType: data.FieldTypeNullableFloat32, want: float32(1.5)},
	{name: "DOUBLE", value: "2.5e-3", fieldType: data.FieldTypeNullableFloat64, want: 2.5e-3},
	{name: "VARCHAR", value: "'héllo ✓'", fieldType: data.FieldTypeNullableString, want: "héllo ✓"},
	{name: "BLOB", value: `'\xDE\xAD\xBE\xEF'::BLOB`, fieldType: data.FieldTypeNullableString, want: "deadbeef"},
	{name: "UUID", value: "'12345678-1234-5678-1234-567812345678'", fieldType: data.FieldTypeNullableString,
		want: "12345678-1234-5678-1234-567812345678"},
	{name: "DATE", value: "DATE '2026-01-02'", fieldType: data.FieldTypeNullableTime, want: utc(2026, 1, 2, 0, 0, 0, 0)},
	{name: "TIME", value: "TIME '01:02:03.5'", fieldType: data.FieldTypeNullableString, want: "01:02:03.5"},
	{name: "TIME_NS", value: "TIME_NS '01:02:03.123456789'", fieldType: data.FieldTypeNullableString, want: "01:02:03.123456789"},
	// By default the server sends a TIMETZ as its time of day, without the offset.
	{name: "TIMETZ", value: "TIMETZ '01:02:03+02'", fieldType: data.FieldTypeNullableString, want: "01:02:03"},
	{name: "TIMESTAMP", value: "TIMESTAMP '2026-01-02 03:04:05.123456'", fieldType: data.FieldTypeNullableTime,
		want: utc(2026, 1, 2, 3, 4, 5, 123456000)},
	{name: "TIMESTAMP_S", value: "TIMESTAMP_S '2026-01-02 03:04:05'", fieldType: data.FieldTypeNullableTime,
		want: utc(2026, 1, 2, 3, 4, 5, 0)},
	{name: "TIMESTAMP_MS", value: "TIMESTAMP_MS '2026-01-02 03:04:05.123'", fieldType: data.FieldTypeNullableTime,
		want: utc(2026, 1, 2, 3, 4, 5, 123000000)},
	{name: "TIMESTAMP_NS", value: "TIMESTAMP_NS '2026-01-02 03:04:05.123456789'", fieldType: data.FieldTypeNullableTime,
		want: utc(2026, 1, 2, 3, 4, 5, 123456789)},
	{name: "TIMESTAMPTZ", value: "TIMESTAMPTZ '2026-01-02 03:04:05.5+02'", fieldType: data.FieldTypeNullableTime,
		want: utc(2026, 1, 2, 1, 4, 5, 500000000)},
	{name: "INTERVAL", value: "INTERVAL '1 year 2 months 3 days 4 hours'", fieldType: data.FieldTypeNullableString,
		want: "1 year 2 months 3 days 04:00:00"},
	{name: "ENUM", column: "ENUM('a', 'b')", value: "'b'", fieldType: data.FieldTypeNullableString, want: "b"},
	{name: "LIST", column: "INTEGER[]", value: "[1, NULL, 3]", fieldType: data.FieldTypeNullableString, want: "[1,null,3]"},
	{name: "ARRAY", column: "INTEGER[3]", value: "[1, 2, 3]", fieldType: data.FieldTypeNullableString, want: "[1,2,3]"},
	{name: "STRUCT", column: "STRUCT(a INTEGER, b VARCHAR)", value: "{'a': 1, 'b': 'x'}", fieldType: data.FieldTypeNullableString,
		want: `{"a":1,"b":"x"}`},
	{name: "MAP", column: "MAP(VARCHAR, INTEGER)", value: "MAP {'k': 1, 'l': NULL}", fieldType: data.FieldTypeNullableString,
		want: `{"k":1,"l":null}`},
	{name: "UNION", column: "UNION(n INTEGER, s VARCHAR)", value: "union_value(s := 'x')", fieldType: data.FieldTypeNullableString,
		want: `{"s":"x"}`},
	{name: "VARIANT", value: "{'a': [1, 2], 'b': 'x'}::VARIANT", fieldType: data.FieldTypeNullableString, want: `{"a":[1,2],"b":"x"}`},
	{name: "JSON", value: `'{"a": [1, 2]}'`, fieldType: data.FieldTypeNullableString, want: `{"a": [1, 2]}`},
	// Without arrow_lossless_conversion a BIT arrives as binary: DuckDB's own bytes.
	{name: "BIT", value: "'101'", fieldType: data.FieldTypeNullableString, want: "05fd"},
	{name: "BIGNUM", value: "12345678901234567890123", fieldType: data.FieldTypeNullableFloat64, want: 12345678901234567890123.0},
	{name: "GEOMETRY", value: "'POINT(1 2)'", fieldType: data.FieldTypeNullableString, want: "POINT (1 2)"},
}

// losslessTypes are the types that arrive differently when the server runs
// with arrow_lossless_conversion: as DuckDB's own Arrow extension types.
var losslessTypes = []typeCase{
	{name: "BOOLEAN", value: "true", fieldType: data.FieldTypeNullableBool, want: true},
	{name: "HUGEINT", value: "-170141183460469231731687303715884105728", fieldType: data.FieldTypeNullableFloat64, want: -math.Pow(2, 127)},
	{name: "UHUGEINT", value: "340282366920938463463374607431768211455", fieldType: data.FieldTypeNullableFloat64, want: math.Pow(2, 128)},
	{name: "UUID", value: "'12345678-1234-5678-1234-567812345678'", fieldType: data.FieldTypeNullableString,
		want: "12345678-1234-5678-1234-567812345678"},
	{name: "TIMETZ", value: "TIMETZ '01:02:03.5-05:30'", fieldType: data.FieldTypeNullableString, want: "01:02:03.5-05:30"},
	{name: "BIT", value: "'1111000011110000111'", fieldType: data.FieldTypeNullableString, want: "1111000011110000111"},
	{name: "BIGNUM", value: "-12345678901234567890123", fieldType: data.FieldTypeNullableFloat64, want: -12345678901234567890123.0},
	{name: "JSON", value: `'{"a": [1, 2]}'`, fieldType: data.FieldTypeNullableString, want: `{"a": [1, 2]}`},
	{name: "VARIANT", value: "[1, 2]::VARIANT", fieldType: data.FieldTypeNullableString, want: "[1,2]"},
	{name: "GEOMETRY", value: "'POINT(1 2)'", fieldType: data.FieldTypeNullableString, want: "POINT (1 2)"},
	{name: "LIST", column: "HUGEINT[]", value: "[1, NULL, 170141183460469231731687303715884105727]", fieldType: data.FieldTypeNullableString,
		want: "[1,null,170141183460469231731687303715884105727]"},
	{name: "STRUCT", column: "STRUCT(b BOOLEAN, u UUID, t TIMETZ, bits BIT, j JSON)",
		value:     `{'b': true, 'u': '12345678-1234-5678-1234-567812345678', 't': TIMETZ '01:02:03+02', 'bits': '101', 'j': '{"k": 1}'}`,
		fieldType: data.FieldTypeNullableString,
		want:      `{"b":true,"u":"12345678-1234-5678-1234-567812345678","t":"01:02:03+02","bits":"101","j":{"k": 1}}`},
}

func sameValue(got, want any) bool {
	if wantTime, ok := want.(time.Time); ok {
		gotTime, ok := got.(time.Time)
		return ok && gotTime.Equal(wantTime) && gotTime.Location() == time.UTC
	}
	return reflect.DeepEqual(got, want)
}

// checkTypes runs every case three ways: a nullable column holding the value
// and a NULL, a NOT NULL column, and a result without rows. The field type
// is the same nullable type each time, and a NULL is nil, never a zero.
func checkTypes(t *testing.T, ds *Datasource, cases []typeCase) {
	for index, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			column := c.columnType()
			table := fmt.Sprintf("live_types_%s_%d", strings.ToLower(strings.TrimRight(strings.Split(c.name, "(")[0], " ")), index)
			checkField := func(frame *data.Frame, rows int) *data.Field {
				t.Helper()
				if len(frame.Fields) != 1 || frame.Rows() != rows {
					t.Fatalf("got %d fields and %d rows, want 1 and %d:\n%s", len(frame.Fields), frame.Rows(), rows, describeFrame(frame))
				}
				field := frame.Fields[0]
				if field.Name != "v" || field.Type() != c.fieldType {
					t.Fatalf("field %q is %s, want %s", field.Name, field.Type(), c.fieldType)
				}
				return field
			}

			// Nullable: the value, then a NULL.
			nullable := mustQuery(t, ds, fmt.Sprintf(
				"SELECT v FROM (SELECT 1 AS n, CAST(%s AS %s) AS v UNION ALL SELECT 2, CAST(NULL AS %s)) ORDER BY n",
				c.value, column, column), queryOptions{})
			field := checkField(nullable, 2)
			if got := cell(field, 0); !sameValue(got, c.want) {
				t.Errorf("nullable column: got %#v, want %#v", got, c.want)
			}
			if got := field.At(1); !reflect.ValueOf(got).IsNil() {
				t.Errorf("a NULL became %#v", cell(field, 1))
			}

			// NOT NULL.
			mustExec(t, ds,
				fmt.Sprintf("CREATE OR REPLACE TABLE %s (v %s NOT NULL)", table, column),
				fmt.Sprintf("INSERT INTO %s VALUES (CAST(%s AS %s))", table, c.value, column))
			field = checkField(mustQuery(t, ds, "SELECT v FROM "+table, queryOptions{}), 1)
			if got := cell(field, 0); !sameValue(got, c.want) {
				t.Errorf("NOT NULL column: got %#v, want %#v", got, c.want)
			}

			// No rows.
			checkField(mustQuery(t, ds, "SELECT v FROM "+table+" WHERE false", queryOptions{}), 0)
			mustExec(t, ds, "DROP TABLE "+table)
		})
	}
}

func TestLiveTypes(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	checkTypes(t, ds, duckDBTypes)

	// The NOT NULL columns above really are NOT NULL.
	mustExec(t, ds, "CREATE OR REPLACE TABLE live_types_constraint (v INTEGER NOT NULL)")
	result := runQuery(t, ds, "INSERT INTO live_types_constraint VALUES (NULL)", queryOptions{})
	wantFailure(t, result, backend.StatusBadRequest, backend.ErrorSourceDownstream, "NOT NULL constraint failed")

	// An untyped NULL column.
	frame := mustQuery(t, ds, "SELECT NULL AS v", queryOptions{})
	if frame.Fields[0].Type() != data.FieldTypeNullableFloat64 || frame.Rows() != 1 || cell(frame.Fields[0], 0) != nil {
		t.Errorf("an untyped NULL:\n%s", describeFrame(frame))
	}
	// Every column of a wide row at once, each NULL: no column shifts another.
	var columns []string
	for index, c := range duckDBTypes {
		columns = append(columns, fmt.Sprintf("CAST(NULL AS %s) AS c%d", c.columnType(), index))
	}
	frame = mustQuery(t, ds, "SELECT "+strings.Join(columns, ", "), queryOptions{})
	for index, c := range duckDBTypes {
		if field := frame.Fields[index]; field.Type() != c.fieldType || cell(field, 0) != nil {
			t.Errorf("%s as a NULL: type %s, value %v", c.name, field.Type(), cell(field, 0))
		}
	}
}

func TestLiveTypesLossless(t *testing.T) {
	ds := newLiveDatasource(t, losslessServer(t), nil, nil)
	// The server really sends the extension types: a BOOLEAN's Arrow type says so.
	frame := mustQuery(t, ds, "SELECT current_setting('arrow_lossless_conversion') AS v", queryOptions{})
	if cell(frame.Fields[0], 0) != true {
		t.Fatalf("arrow_lossless_conversion is %v on the lossless server", cell(frame.Fields[0], 0))
	}
	checkTypes(t, ds, losslessTypes)
}

// What the plugin shows as text is what the server's own CAST(... AS
// VARCHAR) gives.
func TestLiveTextMatchesTheServer(t *testing.T) {
	plain := newLiveDatasource(t, plainServer(t), nil, nil)
	lossless := newLiveDatasource(t, losslessServer(t), nil, nil)
	cases := []struct {
		name   string
		ds     *Datasource
		column string
		values []string
	}{
		{"INTERVAL", plain, "INTERVAL", []string{"'0 seconds'", "'1 day'", "'-1 day'", "'1 month'", "'13 months'", "'25 months 2 days'",
			"'1.5 seconds'", "'-90 seconds'", "'100 hours'", "'1 year 2 months 3 days 4 hours 5 minutes 6.789 seconds'",
			"'-1 year -2 months -3 days -4 hours'", "'1 microsecond'", "'1 month -1 day'"}},
		{"TIME", plain, "TIME", []string{"'00:00:00'", "'01:02:03'", "'23:59:59.999999'", "'12:00:00.5'", "'12:00:00.000001'"}},
		{"TIME_NS", plain, "TIME_NS", []string{"'00:00:00'", "'23:59:59.999999999'", "'12:00:00.5'", "'12:00:00.000000001'"}},
		{"GEOMETRY", plain, "GEOMETRY", []string{"'POINT(1 2)'", "'POINT(-1.5 2.25)'", "'POINT Z (1 2 3)'", "'POINT M (1 2 3)'",
			"'POINT ZM (1 2 3 4)'", "'POINT EMPTY'", "'LINESTRING(0 0, 1 1, 2 0)'", "'LINESTRING EMPTY'", "'LINESTRING Z (1 2 3, 4 5 6)'",
			"'POLYGON((0 0, 4 0, 4 4, 0 4, 0 0), (1 1, 2 1, 2 2, 1 2, 1 1))'", "'POLYGON EMPTY'", "'MULTIPOINT(1 2, 3 4)'",
			"'MULTIPOINT EMPTY'", "'MULTILINESTRING((0 0, 1 1), (2 2, 3 3))'", "'MULTIPOLYGON(((0 0, 1 0, 1 1, 0 0)), ((5 5, 6 5, 6 6, 5 5)))'",
			"'GEOMETRYCOLLECTION(POINT(1 2), LINESTRING(0 0, 1 1))'", "'GEOMETRYCOLLECTION EMPTY'",
			"'POINT(1e20 1e-7)'", "'POINT(123456789.123456789 0.1)'", "'POINT(1234567 0.0001)'", "'POINT(0.00001 1e15)'",
			"'POINT(1e16 -1e16)'", "'POINT(0 -0.5)'", "'MULTIPOINT Z (1 2 3, 4 5 6)'", "'POINT(0.1 0.30000000000000004)'"}},
		{"TIMETZ", lossless, "TIMETZ", []string{"'01:02:03+02'", "'01:02:03.5-05:30'", "'00:00:00+00'", "'23:59:59.999999-15:59'",
			"'12:00:00+15:59:59'", "'12:00:00-00:00:01'"}},
		{"BIT", lossless, "BIT", []string{"'1'", "'0'", "'101'", "'11111111'", "'000000001'", "'1111000011110000111'"}},
		{"UUID", lossless, "UUID", []string{"'00000000-0000-0000-0000-000000000000'", "'ffffffff-ffff-ffff-ffff-ffffffffffff'",
			"'12345678-1234-5678-1234-567812345678'"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rows []string
			for index, value := range c.values {
				rows = append(rows, fmt.Sprintf("(%d, CAST(%s AS %s))", index, value, c.column))
			}
			frame := mustQuery(t, c.ds, "SELECT v, CAST(v AS VARCHAR) AS text FROM (VALUES "+strings.Join(rows, ", ")+
				") t(n, v) ORDER BY n", queryOptions{})
			if frame.Rows() != len(c.values) {
				t.Fatalf("got %d rows, want %d", frame.Rows(), len(c.values))
			}
			for row := range c.values {
				if got, want := cell(frame.Fields[0], row), cell(frame.Fields[1], row); got != want {
					t.Errorf("%s %s: the plugin shows %q, the server %q", c.column, c.values[row], got, want)
				}
			}
		})
	}

	// Numbers: the float64 nearest to the server's exact digits.
	numbers := []struct {
		name   string
		ds     *Datasource
		column string
		values []string
	}{
		{"HUGEINT as a decimal", plain, "HUGEINT", []string{"0", "-1", "9007199254740993", "-99999999999999999999999999999999999999"}},
		{"BIGNUM", plain, "BIGNUM", []string{"0", "-1", "255", "256", "-256", "65536", "12345678901234567890123456789012345678901234567890",
			"-12345678901234567890123456789012345678901234567890"}},
		{"DECIMAL(38,20)", plain, "DECIMAL(38,20)", []string{"0", "-0.00000000000000000001", "123456789012345678.12345678901234567890"}},
		{"HUGEINT", lossless, "HUGEINT", []string{"0", "-1", "170141183460469231731687303715884105727", "-170141183460469231731687303715884105728"}},
		{"UHUGEINT", lossless, "UHUGEINT", []string{"0", "340282366920938463463374607431768211455", "18446744073709551616"}},
	}
	for _, c := range numbers {
		t.Run(c.name, func(t *testing.T) {
			var rows []string
			for index, value := range c.values {
				rows = append(rows, fmt.Sprintf("(%d, CAST('%s' AS %s))", index, value, c.column))
			}
			frame := mustQuery(t, c.ds, "SELECT v, CAST(v AS VARCHAR) AS text FROM (VALUES "+strings.Join(rows, ", ")+
				") t(n, v) ORDER BY n", queryOptions{})
			for row := range c.values {
				want, err := strconv.ParseFloat(cell(frame.Fields[1], row).(string), 64)
				if err != nil {
					t.Fatal(err)
				}
				if got := cell(frame.Fields[0], row); got != want {
					t.Errorf("%s %s: got %v, want %v", c.column, c.values[row], got, want)
				}
			}
		})
	}
}

// Nested values are JSON that says what DuckDB's own to_json says.
func TestLiveNestedJSON(t *testing.T) {
	ds := newLiveDatasource(t, plainServer(t), nil, nil)
	values := []string{
		"[1, 2, 3]",
		"[]::INTEGER[]",
		"[[1, 2], [], NULL, [3]]",
		"['a', NULL, 'quote\" and \\ backslash', 'línea\nnueva']",
		"{'a': 1, 'b': {'c': [true, false, NULL], 'd': 'x'}}",
		"MAP {'k1': [1, 2], 'k2': []}",
		"MAP {1: 'one', 2: 'two'}",
		"MAP {true: 1.5::DOUBLE}",
		"[{'n': 1::TINYINT, 'u': 255::UTINYINT, 'big': 18446744073709551615::UBIGINT, 'f': 1.5::FLOAT, 'd': -2.25::DOUBLE}]",
		"{'dec': 123.45::DECIMAL(9,2), 'huge': 123456789012345678901234567890::HUGEINT}",
		"[union_value(n := 1)::UNION(n INTEGER, s VARCHAR), union_value(s := 'x')::UNION(n INTEGER, s VARCHAR)]",
		"{'e': 'b'::ENUM('a', 'b'), 'l': ['a'::ENUM('a', 'b')]}",
		"[1, 2, 3]::INTEGER[3]",
		"{'v': [1, 2]::VARIANT}",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			frame := mustQuery(t, ds, "SELECT v, CAST(to_json(v) AS VARCHAR) AS server_json FROM (SELECT "+value+" AS v)", queryOptions{})
			got, want := cell(frame.Fields[0], 0).(string), cell(frame.Fields[1], 0).(string)
			if frame.Fields[0].Type() != data.FieldTypeNullableString {
				t.Fatalf("field type %s", frame.Fields[0].Type())
			}
			var gotValue, wantValue any
			if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
				t.Fatalf("the plugin's text is not JSON: %s (%v)", got, err)
			}
			if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
				t.Fatalf("the server's JSON did not parse: %s (%v)", want, err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("the plugin says %s, the server %s", got, want)
			}
		})
	}

	// Where the plugin's JSON says more than the server's: exact text.
	exact := map[string]string{
		"{'t': TIMESTAMP '2026-01-02 03:04:05.123456', 'tz': TIMESTAMPTZ '2026-01-02 03:04:05+02', 'd': DATE '2026-01-02'}": `{"t":"2026-01-02T03:04:05.123456Z","tz":"2026-01-02T01:04:05Z","d":"2026-01-02"}`,
		"{'time': TIME '01:02:03.5', 'iv': INTERVAL '1 day 2 hours', 'blob': '\\xDE\\xAD'::BLOB}":                           `{"time":"01:02:03.5","iv":"1 day 02:00:00","blob":"dead"}`,
		"{'nan': 'NaN'::DOUBLE, 'inf': 'Infinity'::DOUBLE, 'ninf': '-Infinity'::FLOAT}":                                     `{"nan":"NaN","inf":"Infinity","ninf":"-Infinity"}`,
		"MAP {[1, 2]: 'list key'}": `[{"key":[1,2],"value":"list key"}]`,
		"{'u': '12345678-1234-5678-1234-567812345678'::UUID, 'j': '{\"k\": [1]}'::JSON, 'n': NULL}": `{"u":"12345678-1234-5678-1234-567812345678","j":"{\"k\": [1]}","n":null}`,
		"[1.0::DECIMAL(18,3), NULL, 12345678901234567.891::DECIMAL(20,3)]":                          `[1,null,12345678901234567.891]`,
	}
	for value, want := range exact {
		t.Run(value, func(t *testing.T) {
			frame := mustQuery(t, ds, "SELECT "+value+" AS v", queryOptions{})
			got := cell(frame.Fields[0], 0)
			if got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
			if !json.Valid([]byte(got.(string))) {
				t.Errorf("not JSON: %s", got)
			}
		})
	}
}
