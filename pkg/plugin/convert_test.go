package plugin

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

func unhex(t *testing.T, text string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The byte strings below are what hardcharger-server 2.12.0 sent for these
// values (captured with arrow_lossless_conversion on).
func TestDuckDBDecoders(t *testing.T) {
	for raw, want := range map[string]string{
		"80000a029d42b64e76714244cb": "12345678901234567890123",
		"7ffffefa":                   "-5",
		"80000100":                   "0",
		"800001ff":                   "255",
		"8000020100":                 "256",
		"7ffffdfeff":                 "-256",
	} {
		if got, err := decodeBignum(unhex(t, raw)); err != nil || got != want {
			t.Errorf("decodeBignum(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if got, err := decodeHugeint(unhex(t, "05000000000000000000000000000000"), true); err != nil || got != "5" {
		t.Errorf("HUGEINT 5: %q, %v", got, err)
	}
	if got, err := decodeHugeint(unhex(t, "fbffffffffffffffffffffffffffffff"), true); err != nil || got != "-5" {
		t.Errorf("HUGEINT -5: %q, %v", got, err)
	}
	if got, err := decodeHugeint(unhex(t, "ffffffffffffffffffffffffffffffff"), false); err != nil || got != "340282366920938463463374607431768211455" {
		t.Errorf("UHUGEINT max: %q, %v", got, err)
	}
	for raw, want := range map[string]string{"dfc400c078e8dd00": "01:02:03+02", "572e01e019f0dd00": "01:02:03.5-05:30"} {
		if got, err := decodeTimeTZ(unhex(t, raw)); err != nil || got != want {
			t.Errorf("decodeTimeTZ(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, want := range map[string]string{"05fd": "101", "05ff8787": "1111000011110000111"} {
		if got, err := decodeBit(unhex(t, raw)); err != nil || got != want {
			t.Errorf("decodeBit(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	// Malformed values are errors, not guesses.
	if _, err := decodeHugeint([]byte{1, 2}, true); err == nil {
		t.Error("a 2-byte HUGEINT was accepted")
	}
	if _, err := decodeBignum([]byte{0x80}); err == nil {
		t.Error("a 1-byte BIGNUM was accepted")
	}
	if _, err := decodeBit(nil); err == nil {
		t.Error("an empty BIT was accepted")
	}
	if _, err := decodeTimeTZ([]byte{1}); err == nil {
		t.Error("a 1-byte TIMETZ was accepted")
	}
	if _, err := formatUUID([]byte{1}); err == nil {
		t.Error("a 1-byte UUID was accepted")
	}
}

func TestFormatIntervalAndTime(t *testing.T) {
	intervals := []struct {
		months, days, micros int64
		want                 string
	}{
		{0, 0, 0, "00:00:00"},
		{14, 3, 4 * 3600 * 1_000_000, "1 year 2 months 3 days 04:00:00"},
		{1, 0, 0, "1 month"},
		{24, 0, 0, "2 years"},
		{0, 1, 0, "1 day"},
		{0, -1, 0, "-1 day"},
		{0, 0, 1_500_000, "00:00:01.5"},
		{0, 0, -90_000_000, "-00:01:30"},
		{0, 0, 100 * 3600 * 1_000_000, "100:00:00"},
		{-14, -3, -1, "-1 year -2 months -3 days -00:00:00.000001"},
	}
	for _, c := range intervals {
		if got := formatInterval3(c.months, c.days, c.micros); got != c.want {
			t.Errorf("formatInterval3(%d, %d, %d) = %q, want %q", c.months, c.days, c.micros, got, c.want)
		}
	}
	times := []struct {
		value int64
		unit  arrow.TimeUnit
		want  string
	}{
		{3723, arrow.Second, "01:02:03"},
		{3723_500, arrow.Millisecond, "01:02:03.5"},
		{3723_123456, arrow.Microsecond, "01:02:03.123456"},
		{3723_000000, arrow.Microsecond, "01:02:03"},
		{3723_123456789, arrow.Nanosecond, "01:02:03.123456789"},
		{0, arrow.Microsecond, "00:00:00"},
	}
	for _, c := range times {
		if got := formatTimeOfDay(c.value, c.unit); got != c.want {
			t.Errorf("formatTimeOfDay(%d, %s) = %q, want %q", c.value, c.unit, got, c.want)
		}
	}
}

func TestWKBToWKT(t *testing.T) {
	// POINT(1 2) as hardcharger-server sent it.
	raw, err := base64.StdEncoding.DecodeString("AQEAAAAAAAAAAADwPwAAAAAAAABA")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := wkbToWKT(raw); err != nil || got != "POINT (1 2)" {
		t.Errorf("POINT: %q, %v", got, err)
	}
	for _, bad := range [][]byte{nil, {1}, {7, 1, 0, 0, 0}, {1, 99, 0, 0, 0}, raw[:len(raw)-1], append(append([]byte{}, raw...), 0)} {
		if text, err := wkbToWKT(bad); err == nil {
			t.Errorf("wkbToWKT(%x) = %q, want an error", bad, text)
		}
	}
}

// A type without a mapping is an error that names the column and the type;
// it never becomes text.
func TestUnsupportedTypesAreErrors(t *testing.T) {
	unsupported := []arrow.DataType{
		arrow.FixedWidthTypes.Duration_us,
		arrow.FixedWidthTypes.Float16,
		arrow.RunEndEncodedOf(arrow.PrimitiveTypes.Int32, arrow.BinaryTypes.String),
		arrow.ListOf(arrow.FixedWidthTypes.Duration_s),
		arrow.StructOf(arrow.Field{Name: "inner", Type: arrow.MapOf(arrow.BinaryTypes.String, arrow.FixedWidthTypes.Float16)}),
		&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: arrow.FixedWidthTypes.Duration_ms},
	}
	for _, dataType := range unsupported {
		schema := arrow.NewSchema([]arrow.Field{{Name: "ok", Type: arrow.PrimitiveTypes.Int32}, {Name: "odd one", Type: dataType}}, nil)
		_, err := newFrameBuilder(schema, 10)
		var typeErr *unsupportedTypeError
		if !errors.As(err, &typeErr) {
			t.Errorf("%s: got %v, want an unsupported type error", dataType, err)
			continue
		}
		if !strings.Contains(err.Error(), `column "odd one"`) || !strings.Contains(err.Error(), dataType.String()) {
			t.Errorf("%s: the error names neither the column nor the type: %v", dataType, err)
		}
	}
}

// batchOf builds a one-column batch of int64 values start..start+rows-1.
func batchOf(start, rows int64) arrow.RecordBatch {
	builder := array.NewInt64Builder(memory.DefaultAllocator)
	defer builder.Release()
	for i := int64(0); i < rows; i++ {
		builder.Append(start + i)
	}
	column := builder.NewArray()
	defer column.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "n", Type: arrow.PrimitiveTypes.Int64, Nullable: true}}, nil)
	return array.NewRecordBatch(schema, []arrow.Array{column}, rows)
}

func TestRowLimit(t *testing.T) {
	cases := []struct {
		name      string
		limit     int64
		batches   []int64
		rows      int
		truncated bool
		consumed  int // batches read before the reader is told to stop
	}{
		{"under the limit", 10, []int64{3, 4}, 7, false, 2},
		{"exactly the limit", 7, []int64{3, 4}, 7, false, 2},
		{"exactly the limit, then an empty batch", 7, []int64{3, 4, 0}, 7, false, 3},
		{"one row over, in the same batch", 6, []int64{3, 4}, 6, true, 2},
		{"over by a later batch", 7, []int64{3, 4, 5, 6}, 7, true, 3},
		{"a first batch larger than the limit", 2, []int64{100, 100}, 2, true, 1},
		{"no rows", 5, nil, 0, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schema := arrow.NewSchema([]arrow.Field{{Name: "n", Type: arrow.PrimitiveTypes.Int64, Nullable: true}}, nil)
			builder, err := newFrameBuilder(schema, c.limit)
			if err != nil {
				t.Fatal(err)
			}
			consumed, next := 0, int64(0)
			for _, rows := range c.batches {
				batch := batchOf(next, rows)
				next += rows
				more, err := builder.Append(batch)
				batch.Release()
				consumed++
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
			}
			frame := builder.Frame("")
			if frame.Rows() != c.rows || builder.Truncated != c.truncated || consumed != c.consumed {
				t.Fatalf("rows %d (want %d), truncated %v (want %v), batches read %d (want %d)", frame.Rows(), c.rows,
					builder.Truncated, c.truncated, consumed, c.consumed)
			}
			if frame.Fields[0].Type() != data.FieldTypeNullableInt64 {
				t.Errorf("field type %s", frame.Fields[0].Type())
			}
			for row := 0; row < frame.Rows(); row++ {
				if got := cell(frame.Fields[0], row); got != int64(row) {
					t.Fatalf("row %d holds %v", row, got)
				}
			}
		})
	}
}

// Values must be copies: the batch they came from is released before the
// frame is used.
func TestStringsOutliveTheirBatch(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true}}, nil)
	builder, err := newFrameBuilder(schema, 10)
	if err != nil {
		t.Fatal(err)
	}
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	strings := array.NewStringBuilder(allocator)
	strings.AppendValues([]string{"first", "second"}, nil)
	strings.AppendNull()
	column := strings.NewArray()
	strings.Release()
	batch := array.NewRecordBatch(schema, []arrow.Array{column}, 3)
	if _, err := builder.Append(batch); err != nil {
		t.Fatal(err)
	}
	// Overwrite the buffer the values were read from, then release it.
	for _, buffer := range column.Data().Buffers() {
		if buffer != nil {
			for i := range buffer.Bytes() {
				buffer.Bytes()[i] = 'X'
			}
		}
	}
	column.Release()
	batch.Release()
	allocator.AssertSize(t, 0)
	field := builder.Frame("").Fields[0]
	if cell(field, 0) != "first" || cell(field, 1) != "second" || cell(field, 2) != nil {
		t.Errorf("got %v, %v, %v", cell(field, 0), cell(field, 1), cell(field, 2))
	}
}
