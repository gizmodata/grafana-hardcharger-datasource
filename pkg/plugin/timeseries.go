package plugin

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana-plugin-sdk-go/data/sqlutil"
)

// toTimeSeries turns a query's rows into the time series frame Grafana
// graphs. The rows are either wide already (a time column and number
// columns) or long (also text columns, whose values name the series): long
// rows are pivoted with the SDK's LongToWide, so each number column becomes
// one field per combination of the text columns' values, labelled with them.
//
// The time column is the first DATE or TIMESTAMP column or, when there is
// none, a number column named "time" holding a Unix epoch. Rows need not be
// ordered: they are sorted by time. A NULL time is an error.
//
// fill, from $__timeGroup(col, interval, fill), fills the buckets the query
// returned no rows for across the whole time range.
func toTimeSeries(frame *data.Frame, fill *fillSpec, timeRange backend.TimeRange, rowLimit int64) (*data.Frame, error) {
	timeIndex, err := timeFieldIndex(frame)
	if err != nil {
		return nil, err
	}
	times, err := timeValues(frame.Fields[timeIndex])
	if err != nil {
		return nil, err
	}
	// The time column first, without NULLs, and the rows in time order.
	order := make([]int, len(times))
	for i := range order {
		order[i] = i
	}
	sorted := sort.SliceIsSorted(order, func(a, b int) bool { return times[a].Before(times[b]) })
	if !sorted {
		sort.SliceStable(order, func(a, b int) bool { return times[order[a]].Before(times[order[b]]) })
	}
	orderedTimes := make([]time.Time, len(times))
	for row, source := range order {
		orderedTimes[row] = times[source]
	}
	timeField := data.NewField(frame.Fields[timeIndex].Name, nil, orderedTimes)
	fields := []*data.Field{timeField}
	for index, field := range frame.Fields {
		if index == timeIndex {
			continue
		}
		if !sorted {
			field = reorder(field, order)
		}
		fields = append(fields, field)
	}
	out := data.NewFrame(frame.Name, fields...)
	out.RefID = frame.RefID
	out.Meta = frame.Meta
	if out.Meta == nil {
		out.Meta = &data.FrameMeta{}
	}
	out.Meta.PreferredVisualization = data.VisTypeGraph

	schema := out.TimeSeriesSchema()
	if len(schema.ValueIndices) == 0 {
		return nil, userError("the time series format needs at least one number column next to the time column %q",
			timeField.Name)
	}
	if len(times) == 0 {
		return out, nil // no rows: no series
	}
	var fillMissing *data.FillMissing
	if fill != nil {
		fillMissing = &fill.Fill
	}
	if schema.Type == data.TimeSeriesTypeLong {
		wide, err := data.LongToWide(out, fillMissing)
		if err != nil {
			return nil, userError("the rows could not be turned into time series: %s", err)
		}
		wide.RefID = out.RefID
		out = wide
	} else {
		out.Meta.Type = data.FrameTypeTimeSeriesWide
		out.Meta.TypeVersion = data.FrameTypeVersion{0, 1}
	}
	if fill == nil {
		return out, nil
	}

	// Fill the buckets without rows, on the grid the server's buckets are on.
	from := fill.Origin.Add(timeRange.From.Sub(fill.Origin).Truncate(fill.Interval))
	if from.After(timeRange.From) { // a range that starts before the origin
		from = from.Add(-fill.Interval)
	}
	if points := int64(timeRange.To.Sub(from)/fill.Interval) + 1; points > rowLimit {
		return nil, userError("filling gaps every %s over this time range gives %d points per series, more than the "+
			"data source's \"Max rows\" setting (%d): use a longer interval or a shorter time range",
			fill.Interval, points, rowLimit)
	}
	filled, err := sqlutil.ResampleWideFrame(out, fillMissing, backend.TimeRange{From: from, To: timeRange.To}, fill.Interval) //nolint:staticcheck // what Grafana's own SQL data sources fill gaps with
	if err != nil {
		return nil, userError("the gaps could not be filled: %s", err)
	}
	filled.RefID = out.RefID
	return filled, nil
}

// timeFieldIndex finds the time column.
func timeFieldIndex(frame *data.Frame) (int, error) {
	for index, field := range frame.Fields {
		if field.Type() == data.FieldTypeTime || field.Type() == data.FieldTypeNullableTime {
			return index, nil
		}
	}
	for index, field := range frame.Fields {
		if strings.EqualFold(field.Name, "time") && field.Type().Numeric() {
			return index, nil
		}
	}
	return 0, userError("the time series format needs a time column: a DATE or TIMESTAMP column, or a number " +
		"column named \"time\" holding a Unix epoch. Choose the Table format for other results")
}

// timeValues reads the time column: its instants, or its numbers as Unix
// epochs in seconds, milliseconds, microseconds or nanoseconds, told apart
// by their size.
func timeValues(field *data.Field) ([]time.Time, error) {
	times := make([]time.Time, field.Len())
	for row := range times {
		value, ok := field.ConcreteAt(row)
		if !ok {
			return nil, userError("the time column %q is NULL in row %d: filter those rows out (WHERE %s IS NOT NULL)",
				field.Name, row+1, field.Name)
		}
		if instant, isTime := value.(time.Time); isTime {
			times[row] = instant
			continue
		}
		number, err := field.FloatAt(row)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, userError("the time column %q holds %v in row %d, which is not a Unix epoch", field.Name, value, row+1)
		}
		times[row] = epochToTime(number)
	}
	return times, nil
}

// epochToTime reads a Unix epoch whose unit is told by its size: up to 1e11
// seconds (the year 5138), up to 1e14 milliseconds, up to 1e17 microseconds,
// nanoseconds beyond.
func epochToTime(value float64) time.Time {
	magnitude := math.Abs(value)
	switch {
	case magnitude < 1e11:
		seconds, fraction := math.Modf(value)
		return time.Unix(int64(seconds), int64(math.Round(fraction*1e9))).UTC()
	case magnitude < 1e14:
		return time.UnixMilli(int64(value)).UTC()
	case magnitude < 1e17:
		return time.UnixMicro(int64(value)).UTC()
	}
	return time.Unix(0, int64(value)).UTC()
}

// reorder returns field with its rows in the given order.
func reorder(field *data.Field, order []int) *data.Field {
	out := data.NewFieldFromFieldType(field.Type(), len(order))
	out.Name = field.Name
	out.Labels = field.Labels
	out.Config = field.Config
	for row, source := range order {
		out.Set(row, field.At(source))
	}
	return out
}
