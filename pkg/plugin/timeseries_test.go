package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

func ptr[T any](value T) *T { return &value }

func minute(n int) time.Time { return time.Date(2026, 11, 13, 10, n, 0, 0, time.UTC) }

func timePointers(minutes ...int) []*time.Time {
	out := make([]*time.Time, len(minutes))
	for i, m := range minutes {
		out[i] = ptr(minute(m))
	}
	return out
}

var wholeRange = backend.TimeRange{From: minute(0), To: minute(5)}

// column finds a wide frame's value field by name and labels.
func wideField(t *testing.T, frame *data.Frame, name string, labels data.Labels) *data.Field {
	t.Helper()
	for _, field := range frame.Fields {
		if field.Name == name && field.Labels.Equals(labels) {
			return field
		}
	}
	t.Fatalf("no field %s%v in\n%s", name, labels, describeFrame(frame))
	return nil
}

func TestLongToWide(t *testing.T) {
	// Rows out of order, two label columns' worth of series, a NULL value.
	long := data.NewFrame("",
		data.NewField("host", nil, []*string{ptr("b"), ptr("a"), ptr("a"), ptr("b")}),
		data.NewField("ts", nil, timePointers(1, 0, 1, 0)),
		data.NewField("cpu", nil, []*float64{ptr(4.0), ptr(1.0), nil, ptr(3.0)}),
		data.NewField("mem", nil, []*int64{ptr(int64(40)), ptr(int64(10)), ptr(int64(20)), ptr(int64(30))}),
	)
	long.Meta = &data.FrameMeta{ExecutedQueryString: "SELECT ..."}
	long.RefID = "A"
	wide, err := toTimeSeries(long, nil, wholeRange, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if wide.Meta.Type != data.FrameTypeTimeSeriesWide || wide.Meta.ExecutedQueryString != "SELECT ..." ||
		wide.Meta.PreferredVisualization != data.VisTypeGraph || wide.RefID != "A" {
		t.Errorf("meta: %+v, refId %q", wide.Meta, wide.RefID)
	}
	if wide.Rows() != 2 || len(wide.Fields) != 5 {
		t.Fatalf("got\n%s", describeFrame(wide))
	}
	if wide.Fields[0].Name != "ts" || wide.Fields[0].Type() != data.FieldTypeTime ||
		!wide.Fields[0].At(0).(time.Time).Equal(minute(0)) || !wide.Fields[0].At(1).(time.Time).Equal(minute(1)) {
		t.Errorf("time field:\n%s", describeFrame(wide))
	}
	cpuA := wideField(t, wide, "cpu", data.Labels{"host": "a"})
	cpuB := wideField(t, wide, "cpu", data.Labels{"host": "b"})
	memB := wideField(t, wide, "mem", data.Labels{"host": "b"})
	if cell(cpuA, 0) != 1.0 || cell(cpuA, 1) != nil || cell(cpuB, 0) != 3.0 || cell(cpuB, 1) != 4.0 ||
		cell(memB, 0) != int64(30) || cell(memB, 1) != int64(40) {
		t.Errorf("values:\n%s", describeFrame(wide))
	}
}

func TestWideStaysWide(t *testing.T) {
	frame := data.NewFrame("",
		data.NewField("v", nil, []*float64{ptr(2.0), ptr(1.0)}),
		data.NewField("day", nil, timePointers(3, 2)),
	)
	wide, err := toTimeSeries(frame, nil, wholeRange, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if wide.Meta.Type != data.FrameTypeTimeSeriesWide || wide.Fields[0].Name != "day" || wide.Fields[0].Type() != data.FieldTypeTime ||
		cell(wide.Fields[1], 0) != 1.0 || cell(wide.Fields[1], 1) != 2.0 {
		t.Errorf("got\n%s", describeFrame(wide))
	}
}

func TestEpochTimeColumn(t *testing.T) {
	base := minute(0).Unix()
	cases := map[string]*data.Field{
		"seconds":      data.NewField("time", nil, []*int64{ptr(base), ptr(base + 60)}),
		"milliseconds": data.NewField("Time", nil, []*int64{ptr(base * 1000), ptr((base + 60) * 1000)}),
		"microseconds": data.NewField("time", nil, []*int64{ptr(base * 1_000_000), ptr((base + 60) * 1_000_000)}),
		"nanoseconds":  data.NewField("time", nil, []*int64{ptr(base * 1_000_000_000), ptr((base + 60) * 1_000_000_000)}),
		"float":        data.NewField("time", nil, []*float64{ptr(float64(base)), ptr(float64(base + 60))}),
	}
	for name, field := range cases {
		t.Run(name, func(t *testing.T) {
			frame := data.NewFrame("", field, data.NewField("v", nil, []*float64{ptr(1.0), ptr(2.0)}))
			wide, err := toTimeSeries(frame, nil, wholeRange, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if wide.Fields[0].Type() != data.FieldTypeTime || !wide.Fields[0].At(0).(time.Time).Equal(minute(0)) ||
				!wide.Fields[0].At(1).(time.Time).Equal(minute(1)) {
				t.Errorf("got\n%s", describeFrame(wide))
			}
		})
	}
	// A real time column wins over a number named time.
	frame := data.NewFrame("",
		data.NewField("time", nil, []*int64{ptr(int64(5))}),
		data.NewField("ts", nil, timePointers(2)),
	)
	wide, err := toTimeSeries(frame, nil, wholeRange, 1000)
	if err != nil || wide.Fields[0].Name != "ts" {
		t.Errorf("err %v, frame\n%s", err, describeFrame(wide))
	}
}

func TestTimeSeriesErrors(t *testing.T) {
	cases := []struct {
		name  string
		frame *data.Frame
		want  string
	}{
		{"no time column", data.NewFrame("", data.NewField("v", nil, []*float64{ptr(1.0)})), "needs a time column"},
		{"a NULL time", data.NewFrame("", data.NewField("ts", nil, []*time.Time{ptr(minute(0)), nil}),
			data.NewField("v", nil, []*float64{ptr(1.0), ptr(2.0)})), `the time column "ts" is NULL in row 2`},
		{"no number column", data.NewFrame("", data.NewField("ts", nil, timePointers(0)),
			data.NewField("host", nil, []*string{ptr("a")})), "needs at least one number column"},
		{"only a time column", data.NewFrame("", data.NewField("ts", nil, timePointers(0))), "needs at least one number column"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := toTimeSeries(c.frame, nil, wholeRange, 1000)
			failure := redactor{}.classify(t.Context(), err, "")
			if err == nil || failure.Status != backend.StatusBadRequest || failure.Source != backend.ErrorSourceDownstream {
				t.Fatalf("got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestEmptyTimeSeries(t *testing.T) {
	frame := data.NewFrame("",
		data.NewField("host", nil, []*string{}),
		data.NewField("ts", nil, []*time.Time{}),
		data.NewField("v", nil, []*float64{}),
	)
	out, err := toTimeSeries(frame, nil, wholeRange, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows() != 0 || out.Fields[0].Name != "ts" {
		t.Errorf("got\n%s", describeFrame(out))
	}
}

func TestFill(t *testing.T) {
	long := func() *data.Frame {
		frame := data.NewFrame("",
			data.NewField("ts", nil, timePointers(1, 1, 3)),
			data.NewField("host", nil, []*string{ptr("a"), ptr("b"), ptr("a")}),
			data.NewField("v", nil, []*float64{ptr(1.0), ptr(10.0), ptr(3.0)}),
		)
		frame.RefID = "F"
		return frame
	}
	origin := timeBucketOrigin
	cases := []struct {
		name  string
		fill  data.FillMissing
		wantA []any // minutes 0..5
		wantB []any
	}{
		{"NULL", data.FillMissing{Mode: data.FillModeNull}, []any{nil, 1.0, nil, 3.0, nil, nil}, []any{nil, 10.0, nil, nil, nil, nil}},
		{"a value", data.FillMissing{Mode: data.FillModeValue, Value: 0}, []any{0.0, 1.0, 0.0, 3.0, 0.0, 0.0}, []any{0.0, 10.0, 0.0, 0.0, 0.0, 0.0}},
		{"previous", data.FillMissing{Mode: data.FillModePrevious}, []any{nil, 1.0, 1.0, 3.0, 3.0, 3.0}, []any{nil, 10.0, 10.0, 10.0, 10.0, 10.0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The range starts 20 s into a bucket: the grid is the buckets', not the range's.
			timeRange := backend.TimeRange{From: minute(0).Add(20 * time.Second), To: minute(5).Add(20 * time.Second)}
			wide, err := toTimeSeries(long(), &fillSpec{Interval: time.Minute, Fill: c.fill, Origin: origin}, timeRange, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if wide.RefID != "F" || wide.Meta.Type != data.FrameTypeTimeSeriesWide {
				t.Errorf("refId %q, type %q", wide.RefID, wide.Meta.Type)
			}
			if wide.Rows() != 6 {
				t.Fatalf("got %d rows\n%s", wide.Rows(), describeFrame(wide))
			}
			a := wideField(t, wide, "v", data.Labels{"host": "a"})
			b := wideField(t, wide, "v", data.Labels{"host": "b"})
			for row := 0; row < 6; row++ {
				if at := wide.Fields[0].At(row).(time.Time); !at.Equal(minute(row)) {
					t.Errorf("row %d is at %s, want %s", row, at, minute(row))
				}
				if cell(a, row) != c.wantA[row] || cell(b, row) != c.wantB[row] {
					t.Errorf("row %d: a=%v (want %v), b=%v (want %v)", row, cell(a, row), c.wantA[row], cell(b, row), c.wantB[row])
				}
			}
		})
	}
	// More points than the row limit is refused, not computed.
	_, err := toTimeSeries(long(), &fillSpec{Interval: time.Millisecond, Fill: data.FillMissing{Mode: data.FillModeNull}, Origin: origin},
		wholeRange, 1000)
	if err == nil || !strings.Contains(err.Error(), "more than the data source's \"Max rows\" setting (1000)") {
		t.Errorf("a fill of 300,001 points: %v", err)
	}
}
