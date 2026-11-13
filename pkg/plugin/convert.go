package plugin

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// Every Arrow type Hardcharger sends is mapped on purpose (the table is in
// the README), every Grafana field is nullable, and a NULL stays a NULL. A
// type that is not in the table is an error that names it: nothing falls
// back to text.
//
//	BOOLEAN                                   bool
//	TINYINT..BIGINT, UTINYINT..UBIGINT        the same integer type
//	FLOAT, DOUBLE                             float32, float64
//	DECIMAL, HUGEINT, UHUGEINT, BIGNUM        float64 (cast to VARCHAR for every digit)
//	VARCHAR, UUID, JSON, ENUM                 string
//	BLOB, BIT                                 string, hexadecimal
//	DATE, TIMESTAMP (any unit), TIMESTAMPTZ   time (a TIMESTAMP without a zone is read as UTC)
//	TIME, TIMETZ, INTERVAL                    string, as DuckDB prints them
//	LIST, ARRAY, STRUCT, MAP, UNION, VARIANT  string, JSON
//	GEOMETRY                                  string, WKT
//	NULL (an untyped NULL column)             float64, all NULL

// unsupportedTypeError is a column whose Arrow type the plugin has no
// mapping for.
type unsupportedTypeError struct {
	column   string
	dataType string
}

func (e *unsupportedTypeError) Error() string {
	return fmt.Sprintf("column %q has the Arrow type %s, which this data source does not map to a Grafana type: "+
		"CAST it in the query (to VARCHAR, for example)", e.column, e.dataType)
}

// reader reads row i of one Arrow array: its value, or valid == false for a
// NULL.
type reader[T any] func(i int) (value T, valid bool, err error)

// binder makes the reader of one array (a batch's column).
type binder[T any] func(arr arrow.Array) (reader[T], error)

// column collects one result column, batch by batch, into a Grafana field.
type column struct {
	name     string
	appendFn func(arr arrow.Array, rows int) error
	fieldFn  func() *data.Field
}

func typedColumn[T any](name string, bind binder[T]) *column {
	values := []*T{}
	return &column{
		name: name,
		appendFn: func(arr arrow.Array, rows int) error {
			read, err := bind(arr)
			if err != nil {
				return err
			}
			for i := 0; i < rows; i++ {
				value, valid, err := read(i)
				if err != nil {
					return err
				}
				if !valid {
					values = append(values, nil)
					continue
				}
				values = append(values, &value)
			}
			return nil
		},
		fieldFn: func() *data.Field { return data.NewField(name, nil, values) },
	}
}

func typeMismatch(arr arrow.Array, want string) error {
	return fmt.Errorf("unexpected Arrow array %T (%s) where %s was announced", arr, arr.DataType(), want)
}

// primitive reads an array whose values are already the Grafana type.
func primitive[T any, A interface {
	arrow.Array
	Value(int) T
}](arr arrow.Array) (reader[T], error) {
	typed, ok := arr.(A)
	if !ok {
		var zero T
		return nil, typeMismatch(arr, fmt.Sprintf("%T", zero))
	}
	return func(i int) (T, bool, error) {
		if typed.IsNull(i) {
			var zero T
			return zero, false, nil
		}
		return typed.Value(i), true, nil
	}, nil
}

// throughDictionary makes a binder read dictionary-encoded arrays (DuckDB's
// ENUM) by their values.
func throughDictionary[T any](bind binder[T]) binder[T] {
	return func(arr arrow.Array) (reader[T], error) {
		dict, ok := arr.(*array.Dictionary)
		if !ok {
			return bind(arr)
		}
		read, err := bind(dict.Dictionary())
		if err != nil {
			return nil, err
		}
		return func(i int) (T, bool, error) {
			if dict.IsNull(i) {
				var zero T
				return zero, false, nil
			}
			return read(dict.GetValueIndex(i))
		}, nil
	}
}

// newColumn chooses a result column's mapping from its Arrow field.
func newColumn(field arrow.Field) (*column, error) {
	name := field.Name
	dataType := field.Type
	if dict, ok := dataType.(*arrow.DictionaryType); ok {
		dataType = dict.ValueType
	}
	unsupported := func() (*column, error) {
		return nil, &unsupportedTypeError{column: name, dataType: field.Type.String()}
	}
	switch dataType.ID() {
	case arrow.NULL:
		return typedColumn(name, func(arr arrow.Array) (reader[float64], error) {
			return func(int) (float64, bool, error) { return 0, false, nil }, nil
		}), nil
	case arrow.BOOL:
		return typedColumn(name, throughDictionary(primitive[bool, *array.Boolean])), nil
	case arrow.INT8:
		return typedColumn(name, throughDictionary(primitive[int8, *array.Int8])), nil
	case arrow.INT16:
		return typedColumn(name, throughDictionary(primitive[int16, *array.Int16])), nil
	case arrow.INT32:
		return typedColumn(name, throughDictionary(primitive[int32, *array.Int32])), nil
	case arrow.INT64:
		return typedColumn(name, throughDictionary(primitive[int64, *array.Int64])), nil
	case arrow.UINT8:
		return typedColumn(name, throughDictionary(primitive[uint8, *array.Uint8])), nil
	case arrow.UINT16:
		return typedColumn(name, throughDictionary(primitive[uint16, *array.Uint16])), nil
	case arrow.UINT32:
		return typedColumn(name, throughDictionary(primitive[uint32, *array.Uint32])), nil
	case arrow.UINT64:
		return typedColumn(name, throughDictionary(primitive[uint64, *array.Uint64])), nil
	case arrow.FLOAT32:
		return typedColumn(name, throughDictionary(primitive[float32, *array.Float32])), nil
	case arrow.FLOAT64:
		return typedColumn(name, throughDictionary(primitive[float64, *array.Float64])), nil
	case arrow.DECIMAL32, arrow.DECIMAL64, arrow.DECIMAL128, arrow.DECIMAL256:
		return typedColumn(name, throughDictionary(bindDecimal)), nil
	case arrow.DATE32, arrow.DATE64, arrow.TIMESTAMP:
		return typedColumn(name, throughDictionary(bindTime)), nil
	case arrow.BINARY, arrow.LARGE_BINARY, arrow.BINARY_VIEW, arrow.FIXED_SIZE_BINARY:
		if extensionName(field) == "geoarrow.wkb" {
			return typedColumn(name, throughDictionary(bindGeometry)), nil
		}
		return typedColumn(name, throughDictionary(bindText)), nil
	case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW,
		arrow.TIME32, arrow.TIME64,
		arrow.INTERVAL_MONTHS, arrow.INTERVAL_DAY_TIME, arrow.INTERVAL_MONTH_DAY_NANO:
		return typedColumn(name, throughDictionary(bindText)), nil
	case arrow.LIST, arrow.LARGE_LIST, arrow.FIXED_SIZE_LIST, arrow.LIST_VIEW, arrow.LARGE_LIST_VIEW,
		arrow.STRUCT, arrow.MAP, arrow.SPARSE_UNION, arrow.DENSE_UNION:
		if err := checkJSONType(dataType); err != nil {
			return unsupported()
		}
		return typedColumn(name, throughDictionary(bindJSONText)), nil
	case arrow.EXTENSION:
		ext := dataType.(arrow.ExtensionType)
		switch ext.ExtensionName() {
		case "arrow.bool8":
			return typedColumn(name, throughDictionary(primitive[bool, *extensions.Bool8Array])), nil
		case "arrow.uuid", "arrow.json":
			return typedColumn(name, throughDictionary(bindText)), nil
		case "arrow.parquet.variant":
			return typedColumn(name, throughDictionary(bindJSONText)), nil
		case "arrow.opaque":
			opaque, ok := ext.(*extensions.OpaqueType)
			if !ok || !strings.EqualFold(opaque.VendorName, "DuckDB") {
				return unsupported()
			}
			switch opaque.TypeName {
			case "hugeint", "uhugeint", "bignum":
				return typedColumn(name, throughDictionary(bindDecimal)), nil
			case "bit", "time_tz":
				return typedColumn(name, throughDictionary(bindText)), nil
			}
		}
	}
	return unsupported()
}

// extensionName is the extension a field's metadata names, for extension
// types Arrow's Go library has not registered (geoarrow.wkb).
func extensionName(field arrow.Field) string {
	if index := field.Metadata.FindKey("ARROW:extension:name"); index >= 0 {
		return field.Metadata.Values()[index]
	}
	return ""
}

// bindDecimal reads DECIMAL of any width, and DuckDB's HUGEINT, UHUGEINT and
// BIGNUM, as float64: the nearest one to the exact value.
func bindDecimal(arr arrow.Array) (reader[float64], error) {
	exact, err := bindExactNumber(arr)
	if err != nil {
		return nil, err
	}
	return func(i int) (float64, bool, error) {
		text, valid, err := exact(i)
		if err != nil || !valid {
			return 0, false, err
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil && value == 0 { // a range error still gives ±Inf, the nearest float64
			return 0, false, fmt.Errorf("could not read the number %q: %w", text, err)
		}
		return value, true, nil
	}, nil
}

// bindExactNumber reads the same types as their exact decimal text.
func bindExactNumber(arr arrow.Array) (reader[string], error) {
	if ext, ok := arr.(array.ExtensionArray); ok {
		opaque, ok := ext.ExtensionType().(*extensions.OpaqueType)
		if !ok {
			return nil, typeMismatch(arr, "a number")
		}
		storage := ext.Storage()
		bytesAt, err := bindBytes(storage)
		if err != nil {
			return nil, err
		}
		var decode func([]byte) (string, error)
		switch opaque.TypeName {
		case "hugeint":
			decode = func(b []byte) (string, error) { return decodeHugeint(b, true) }
		case "uhugeint":
			decode = func(b []byte) (string, error) { return decodeHugeint(b, false) }
		case "bignum":
			decode = decodeBignum
		default:
			return nil, typeMismatch(arr, "a number")
		}
		return func(i int) (string, bool, error) {
			if arr.IsNull(i) {
				return "", false, nil
			}
			text, err := decode(bytesAt(i))
			return text, err == nil, err
		}, nil
	}
	switch arr.DataType().ID() {
	case arrow.DECIMAL32, arrow.DECIMAL64, arrow.DECIMAL128, arrow.DECIMAL256:
		return func(i int) (string, bool, error) {
			if arr.IsNull(i) {
				return "", false, nil
			}
			return arr.ValueStr(i), true, nil
		}, nil
	}
	return nil, typeMismatch(arr, "a number")
}

// bindBytes reads any binary array's values.
func bindBytes(arr arrow.Array) (func(i int) []byte, error) {
	switch typed := arr.(type) {
	case *array.Binary:
		return typed.Value, nil
	case *array.LargeBinary:
		return typed.Value, nil
	case *array.BinaryView:
		return typed.Value, nil
	case *array.FixedSizeBinary:
		return typed.Value, nil
	}
	return nil, typeMismatch(arr, "binary")
}

// bindTime reads DATE and TIMESTAMP columns. A timestamp is an instant: one
// without a time zone is read as UTC, one with a zone already is an instant.
func bindTime(arr arrow.Array) (reader[time.Time], error) {
	var at func(i int) time.Time
	switch typed := arr.(type) {
	case *array.Date32:
		at = func(i int) time.Time { return typed.Value(i).ToTime() }
	case *array.Date64:
		at = func(i int) time.Time { return typed.Value(i).ToTime() }
	case *array.Timestamp:
		unit := typed.DataType().(*arrow.TimestampType).Unit
		at = func(i int) time.Time { return typed.Value(i).ToTime(unit) }
	default:
		return nil, typeMismatch(arr, "a date or timestamp")
	}
	return func(i int) (time.Time, bool, error) {
		if arr.IsNull(i) {
			return time.Time{}, false, nil
		}
		return at(i).UTC(), true, nil
	}, nil
}

// bindGeometry reads GEOMETRY (well-known binary) as well-known text; a
// value that is not well-known binary this plugin understands is shown in
// hexadecimal.
func bindGeometry(arr arrow.Array) (reader[string], error) {
	bytesAt, err := bindBytes(arr)
	if err != nil {
		return nil, err
	}
	return func(i int) (string, bool, error) {
		if arr.IsNull(i) {
			return "", false, nil
		}
		raw := bytesAt(i)
		if text, err := wkbToWKT(raw); err == nil {
			return text, true, nil
		}
		return hex.EncodeToString(raw), true, nil
	}, nil
}

// bindText reads the types shown as text: strings, binary (hexadecimal),
// times of day and intervals (as DuckDB prints them), and the UUID, JSON,
// BIT and TIMETZ extension types.
func bindText(arr arrow.Array) (reader[string], error) {
	var at func(i int) (string, error)
	switch typed := arr.(type) {
	case *array.String:
		// Copied: Value aliases the batch's buffer, which is released.
		at = func(i int) (string, error) { return strings.Clone(typed.Value(i)), nil }
	case *array.LargeString:
		at = func(i int) (string, error) { return strings.Clone(typed.Value(i)), nil }
	case *array.StringView:
		at = func(i int) (string, error) { return strings.Clone(typed.Value(i)), nil }
	case *array.Binary, *array.LargeBinary, *array.BinaryView, *array.FixedSizeBinary:
		bytesAt, err := bindBytes(arr)
		if err != nil {
			return nil, err
		}
		at = func(i int) (string, error) { return hex.EncodeToString(bytesAt(i)), nil }
	case *array.Time32:
		unit := typed.DataType().(*arrow.Time32Type).Unit
		at = func(i int) (string, error) { return formatTimeOfDay(int64(typed.Value(i)), unit), nil }
	case *array.Time64:
		unit := typed.DataType().(*arrow.Time64Type).Unit
		at = func(i int) (string, error) { return formatTimeOfDay(int64(typed.Value(i)), unit), nil }
	case *array.MonthInterval:
		at = func(i int) (string, error) { return formatInterval3(int64(typed.Value(i)), 0, 0), nil }
	case *array.DayTimeInterval:
		at = func(i int) (string, error) {
			value := typed.Value(i)
			return formatInterval3(0, int64(value.Days), int64(value.Milliseconds)*1000), nil
		}
	case *array.MonthDayNanoInterval:
		at = func(i int) (string, error) {
			value := typed.Value(i)
			return formatInterval3(int64(value.Months), int64(value.Days), value.Nanoseconds/1000), nil
		}
	case array.ExtensionArray:
		storage := typed.Storage()
		switch ext := typed.ExtensionType().(type) {
		case *extensions.UUIDType:
			bytesAt, err := bindBytes(storage)
			if err != nil {
				return nil, err
			}
			at = func(i int) (string, error) { return formatUUID(bytesAt(i)) }
		case *extensions.JSONType:
			return bindText(storage)
		case *extensions.OpaqueType:
			bytesAt, err := bindBytes(storage)
			if err != nil {
				return nil, err
			}
			switch ext.TypeName {
			case "bit":
				at = func(i int) (string, error) { return decodeBit(bytesAt(i)) }
			case "time_tz":
				at = func(i int) (string, error) { return decodeTimeTZ(bytesAt(i)) }
			default:
				return nil, typeMismatch(arr, "text")
			}
		default:
			return nil, typeMismatch(arr, "text")
		}
	default:
		return nil, typeMismatch(arr, "text")
	}
	return func(i int) (string, bool, error) {
		if arr.IsNull(i) {
			return "", false, nil
		}
		text, err := at(i)
		return text, err == nil, err
	}, nil
}

// bindJSONText reads nested values (and VARIANT) as JSON text.
func bindJSONText(arr arrow.Array) (reader[string], error) {
	write, err := bindJSON(arr)
	if err != nil {
		return nil, err
	}
	null := nullCheck(arr)
	var buf strings.Builder
	return func(i int) (string, bool, error) {
		if null(i) {
			return "", false, nil
		}
		buf.Reset()
		if err := write(&buf, i); err != nil {
			return "", false, err
		}
		return buf.String(), true, nil
	}, nil
}

// frameBuilder collects a result's rows into a Grafana frame, up to a row
// limit.
type frameBuilder struct {
	columns []*column
	rows    int64
	limit   int64
	// Truncated: the result had more rows than the limit.
	Truncated bool
}

// newFrameBuilder maps every column of schema, or fails naming the first
// column whose type has no mapping.
func newFrameBuilder(schema *arrow.Schema, limit int64) (*frameBuilder, error) {
	builder := &frameBuilder{limit: limit}
	for _, field := range schema.Fields() {
		column, err := newColumn(field)
		if err != nil {
			return nil, err
		}
		builder.columns = append(builder.columns, column)
	}
	return builder, nil
}

// Append adds a batch's rows. It reports false once the limit is reached and
// the result has more rows than it: the caller stops reading.
func (b *frameBuilder) Append(batch arrow.RecordBatch) (more bool, err error) {
	rows := batch.NumRows()
	if rows == 0 {
		return true, nil
	}
	if room := b.limit - b.rows; rows > room {
		rows = room
		b.Truncated = true
	}
	if int(batch.NumCols()) != len(b.columns) {
		return false, fmt.Errorf("a batch has %d columns, its schema %d", batch.NumCols(), len(b.columns))
	}
	if rows > 0 {
		for index, column := range b.columns {
			if err := column.appendFn(batch.Column(index), int(rows)); err != nil {
				return false, fmt.Errorf("column %q: %w", column.name, err)
			}
		}
		b.rows += rows
	}
	return !b.Truncated, nil
}

// Rows is how many rows were collected.
func (b *frameBuilder) Rows() int64 { return b.rows }

// Frame is the collected rows.
func (b *frameBuilder) Frame(name string) *data.Frame {
	frame := data.NewFrame(name)
	for _, column := range b.columns {
		frame.Fields = append(frame.Fields, column.fieldFn())
	}
	return frame
}
