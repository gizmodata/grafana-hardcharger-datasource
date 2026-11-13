package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/extensions"
)

// Nested values (LIST, ARRAY, STRUCT, MAP, UNION) and VARIANT are shown as
// JSON text: a list as an array, a struct as an object in field order, a map
// as an object (an array of {"key", "value"} when its keys are nested), a
// union as {"<member>": value}. Inside it numbers are JSON numbers with
// every digit, timestamps are RFC 3339 in UTC, dates are YYYY-MM-DD, and
// what the plugin shows as text elsewhere (times of day, intervals, binary
// in hexadecimal) is a JSON string.

// jsonWriter writes row i of one array as JSON (null for a NULL).
type jsonWriter func(out *strings.Builder, i int) error

// checkJSONType reports whether every type nested in dataType can be written
// as JSON.
func checkJSONType(dataType arrow.DataType) error {
	switch typed := dataType.(type) {
	case *arrow.DictionaryType:
		return checkJSONType(typed.ValueType)
	case arrow.ExtensionType:
		switch typed.ExtensionName() {
		case "arrow.bool8", "arrow.uuid", "arrow.json", "arrow.parquet.variant":
			return nil
		case "arrow.opaque":
			if opaque, ok := typed.(*extensions.OpaqueType); ok && strings.EqualFold(opaque.VendorName, "DuckDB") {
				switch opaque.TypeName {
				case "hugeint", "uhugeint", "bignum", "bit", "time_tz":
					return nil
				}
			}
		}
		return fmt.Errorf("unsupported nested type %s", dataType)
	case *arrow.MapType:
		if err := checkJSONType(typed.KeyType()); err != nil {
			return err
		}
		return checkJSONType(typed.ItemType())
	case arrow.ListLikeType:
		return checkJSONType(typed.Elem())
	case *arrow.StructType:
		for _, field := range typed.Fields() {
			if err := checkJSONType(field.Type); err != nil {
				return err
			}
		}
		return nil
	case arrow.UnionType:
		for _, field := range typed.Fields() {
			if err := checkJSONType(field.Type); err != nil {
				return err
			}
		}
		return nil
	}
	switch dataType.ID() {
	case arrow.NULL, arrow.BOOL,
		arrow.INT8, arrow.INT16, arrow.INT32, arrow.INT64,
		arrow.UINT8, arrow.UINT16, arrow.UINT32, arrow.UINT64,
		arrow.FLOAT32, arrow.FLOAT64,
		arrow.DECIMAL32, arrow.DECIMAL64, arrow.DECIMAL128, arrow.DECIMAL256,
		arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW,
		arrow.BINARY, arrow.LARGE_BINARY, arrow.BINARY_VIEW, arrow.FIXED_SIZE_BINARY,
		arrow.DATE32, arrow.DATE64, arrow.TIMESTAMP, arrow.TIME32, arrow.TIME64,
		arrow.INTERVAL_MONTHS, arrow.INTERVAL_DAY_TIME, arrow.INTERVAL_MONTH_DAY_NANO:
		return nil
	}
	return fmt.Errorf("unsupported nested type %s", dataType)
}

func writeJSONString(out *strings.Builder, text string) {
	encoded, _ := json.Marshal(text) // a string always marshals
	out.Write(encoded)
}

func writeJSONFloat(out *strings.Builder, value float64) {
	switch {
	case math.IsNaN(value):
		out.WriteString(`"NaN"`)
	case math.IsInf(value, 1):
		out.WriteString(`"Infinity"`)
	case math.IsInf(value, -1):
		out.WriteString(`"-Infinity"`)
	default:
		encoded, _ := json.Marshal(value)
		out.Write(encoded)
	}
}

// integerWriter writes any integer array's values as JSON numbers.
func integerWriter[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64, A interface {
	arrow.Array
	Value(int) T
}](arr A, format func(T) string) jsonWriter {
	return func(out *strings.Builder, i int) error {
		out.WriteString(format(arr.Value(i)))
		return nil
	}
}

func formatSigned[T int8 | int16 | int32 | int64](value T) string {
	return strconv.FormatInt(int64(value), 10)
}

func formatUnsigned[T uint8 | uint16 | uint32 | uint64](value T) string {
	return strconv.FormatUint(uint64(value), 10)
}

// bindJSON makes the JSON writer of one array.
func bindJSON(arr arrow.Array) (jsonWriter, error) {
	write, err := bindJSONValue(arr)
	if err != nil {
		return nil, err
	}
	null := nullCheck(arr)
	return func(out *strings.Builder, i int) error {
		if null(i) {
			out.WriteString("null")
			return nil
		}
		return write(out, i)
	}, nil
}

// nullCheck reports which rows of an array are NULL. A union has no
// validity of its own: a NULL union arrives as a member that is NULL.
func nullCheck(arr arrow.Array) func(i int) bool {
	union, ok := arr.(array.Union)
	if !ok {
		return arr.IsNull
	}
	dense, _ := arr.(*array.DenseUnion)
	return func(i int) bool {
		child := union.ChildID(i)
		if child < 0 || child >= union.NumFields() {
			return false
		}
		row := i
		if dense != nil {
			row = int(dense.ValueOffset(i))
		}
		return union.Field(child).IsNull(row)
	}
}

// textWriter writes, as a JSON string, what reader shows as text.
func textWriter(arr arrow.Array, bind binder[string]) (jsonWriter, error) {
	read, err := bind(arr)
	if err != nil {
		return nil, err
	}
	return func(out *strings.Builder, i int) error {
		text, valid, err := read(i)
		if err != nil {
			return err
		}
		if !valid {
			out.WriteString("null")
			return nil
		}
		writeJSONString(out, text)
		return nil
	}, nil
}

// bindJSONValue writes non-NULL values; bindJSON handles the NULLs.
func bindJSONValue(arr arrow.Array) (jsonWriter, error) {
	switch typed := arr.(type) {
	case *array.Null:
		return func(out *strings.Builder, _ int) error { out.WriteString("null"); return nil }, nil
	case *array.Boolean:
		return func(out *strings.Builder, i int) error {
			out.WriteString(strconv.FormatBool(typed.Value(i)))
			return nil
		}, nil
	case *array.Int8:
		return integerWriter(typed, formatSigned[int8]), nil
	case *array.Int16:
		return integerWriter(typed, formatSigned[int16]), nil
	case *array.Int32:
		return integerWriter(typed, formatSigned[int32]), nil
	case *array.Int64:
		return integerWriter(typed, formatSigned[int64]), nil
	case *array.Uint8:
		return integerWriter(typed, formatUnsigned[uint8]), nil
	case *array.Uint16:
		return integerWriter(typed, formatUnsigned[uint16]), nil
	case *array.Uint32:
		return integerWriter(typed, formatUnsigned[uint32]), nil
	case *array.Uint64:
		return integerWriter(typed, formatUnsigned[uint64]), nil
	case *array.Float32:
		return func(out *strings.Builder, i int) error {
			value := typed.Value(i)
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				writeJSONFloat(out, float64(value))
				return nil
			}
			out.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
			return nil
		}, nil
	case *array.Float64:
		return func(out *strings.Builder, i int) error { writeJSONFloat(out, typed.Value(i)); return nil }, nil
	case *array.Date32:
		return func(out *strings.Builder, i int) error {
			writeJSONString(out, typed.Value(i).ToTime().UTC().Format(time.DateOnly))
			return nil
		}, nil
	case *array.Date64:
		return func(out *strings.Builder, i int) error {
			writeJSONString(out, typed.Value(i).ToTime().UTC().Format(time.DateOnly))
			return nil
		}, nil
	case *array.Timestamp:
		unit := typed.DataType().(*arrow.TimestampType).Unit
		return func(out *strings.Builder, i int) error {
			writeJSONString(out, typed.Value(i).ToTime(unit).UTC().Format(time.RFC3339Nano))
			return nil
		}, nil
	case *array.String, *array.LargeString, *array.StringView,
		*array.Binary, *array.LargeBinary, *array.BinaryView, *array.FixedSizeBinary,
		*array.Time32, *array.Time64,
		*array.MonthInterval, *array.DayTimeInterval, *array.MonthDayNanoInterval:
		return textWriter(arr, bindText)
	case *array.Dictionary:
		values, err := bindJSON(typed.Dictionary())
		if err != nil {
			return nil, err
		}
		return func(out *strings.Builder, i int) error { return values(out, typed.GetValueIndex(i)) }, nil
	case *array.Map:
		return bindJSONMap(typed)
	case array.ListLike:
		elements, err := bindJSON(typed.ListValues())
		if err != nil {
			return nil, err
		}
		return func(out *strings.Builder, i int) error {
			start, end := typed.ValueOffsets(i)
			out.WriteByte('[')
			for j := start; j < end; j++ {
				if j > start {
					out.WriteByte(',')
				}
				if err := elements(out, int(j)); err != nil {
					return err
				}
			}
			out.WriteByte(']')
			return nil
		}, nil
	case *array.Struct:
		fields := typed.DataType().(*arrow.StructType).Fields()
		writers := make([]jsonWriter, len(fields))
		for index := range fields {
			writer, err := bindJSON(typed.Field(index))
			if err != nil {
				return nil, err
			}
			writers[index] = writer
		}
		return func(out *strings.Builder, i int) error {
			out.WriteByte('{')
			for index, field := range fields {
				if index > 0 {
					out.WriteByte(',')
				}
				writeJSONString(out, field.Name)
				out.WriteByte(':')
				if err := writers[index](out, i); err != nil {
					return err
				}
			}
			out.WriteByte('}')
			return nil
		}, nil
	case array.Union:
		return bindJSONUnion(typed)
	case array.ExtensionArray:
		return bindJSONExtension(typed)
	}
	if id := arr.DataType().ID(); id == arrow.DECIMAL32 || id == arrow.DECIMAL64 || id == arrow.DECIMAL128 ||
		id == arrow.DECIMAL256 {
		return func(out *strings.Builder, i int) error { out.WriteString(arr.ValueStr(i)); return nil }, nil
	}
	return nil, fmt.Errorf("unsupported nested type %s", arr.DataType())
}

func bindJSONMap(arr *array.Map) (jsonWriter, error) {
	items, err := bindJSON(arr.Items())
	if err != nil {
		return nil, err
	}
	keys, err := bindJSON(arr.Keys())
	if err != nil {
		return nil, err
	}
	switch arr.Keys().DataType().ID() {
	case arrow.LIST, arrow.LARGE_LIST, arrow.FIXED_SIZE_LIST, arrow.LIST_VIEW, arrow.LARGE_LIST_VIEW,
		arrow.STRUCT, arrow.MAP, arrow.SPARSE_UNION, arrow.DENSE_UNION:
		// Nested keys cannot name an object's members.
		return func(out *strings.Builder, i int) error {
			start, end := arr.ValueOffsets(i)
			out.WriteByte('[')
			for j := start; j < end; j++ {
				if j > start {
					out.WriteByte(',')
				}
				out.WriteString(`{"key":`)
				if err := keys(out, int(j)); err != nil {
					return err
				}
				out.WriteString(`,"value":`)
				if err := items(out, int(j)); err != nil {
					return err
				}
				out.WriteByte('}')
			}
			out.WriteByte(']')
			return nil
		}, nil
	}
	var key strings.Builder
	return func(out *strings.Builder, i int) error {
		start, end := arr.ValueOffsets(i)
		out.WriteByte('{')
		for j := start; j < end; j++ {
			if j > start {
				out.WriteByte(',')
			}
			key.Reset()
			if err := keys(&key, int(j)); err != nil {
				return err
			}
			// A key that is a number or a boolean becomes its text, as in
			// DuckDB's own JSON.
			if text := key.String(); strings.HasPrefix(text, `"`) {
				out.WriteString(text)
			} else {
				writeJSONString(out, text)
			}
			out.WriteByte(':')
			if err := items(out, int(j)); err != nil {
				return err
			}
		}
		out.WriteByte('}')
		return nil
	}, nil
}

func bindJSONUnion(arr array.Union) (jsonWriter, error) {
	fields := arr.UnionType().Fields()
	writers := make([]jsonWriter, len(fields))
	for index := range fields {
		writer, err := bindJSON(arr.Field(index))
		if err != nil {
			return nil, err
		}
		writers[index] = writer
	}
	dense, _ := arr.(*array.DenseUnion)
	return func(out *strings.Builder, i int) error {
		child := arr.ChildID(i)
		if child < 0 || child >= len(fields) {
			return fmt.Errorf("a union value names member %d of %d", child, len(fields))
		}
		row := i
		if dense != nil {
			row = int(dense.ValueOffset(i))
		}
		out.WriteByte('{')
		writeJSONString(out, fields[child].Name)
		out.WriteByte(':')
		if err := writers[child](out, row); err != nil {
			return err
		}
		out.WriteByte('}')
		return nil
	}, nil
}

func bindJSONExtension(arr array.ExtensionArray) (jsonWriter, error) {
	switch typed := arr.(type) {
	case *extensions.Bool8Array:
		return func(out *strings.Builder, i int) error {
			out.WriteString(strconv.FormatBool(typed.Value(i)))
			return nil
		}, nil
	case *extensions.VariantArray:
		return func(out *strings.Builder, i int) error {
			value, err := typed.Value(i)
			if err != nil {
				return fmt.Errorf("reading a VARIANT value: %w", err)
			}
			encoded, err := value.MarshalJSON()
			if err != nil {
				return fmt.Errorf("writing a VARIANT value as JSON: %w", err)
			}
			out.Write(encoded)
			return nil
		}, nil
	case *extensions.JSONArray:
		// JSON inside JSON stays JSON when it is valid; otherwise it is a string.
		read, err := bindText(arr)
		if err != nil {
			return nil, err
		}
		return func(out *strings.Builder, i int) error {
			text, _, err := read(i)
			if err != nil {
				return err
			}
			if json.Valid([]byte(text)) {
				out.WriteString(text)
			} else {
				writeJSONString(out, text)
			}
			return nil
		}, nil
	}
	if opaque, ok := arr.ExtensionType().(*extensions.OpaqueType); ok {
		switch opaque.TypeName {
		case "hugeint", "uhugeint", "bignum":
			exact, err := bindExactNumber(arr)
			if err != nil {
				return nil, err
			}
			return func(out *strings.Builder, i int) error {
				text, _, err := exact(i)
				out.WriteString(text)
				return err
			}, nil
		}
	}
	return textWriter(arr, bindText)
}
