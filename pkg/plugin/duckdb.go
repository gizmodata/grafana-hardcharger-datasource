package plugin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// DuckDB's text forms of the types Grafana has no type for, and the decoders
// of the opaque Arrow extension types DuckDB sends when the server runs with
// arrow_lossless_conversion (HUGEINT, UHUGEINT, BIT, TIMETZ) or always
// (BIGNUM). Each is checked against the server's own CAST(... AS VARCHAR) in
// the live tests.

// formatTimeOfDay writes a time of day as DuckDB does: HH:MM:SS, then the
// fraction without its trailing zeros.
func formatTimeOfDay(value int64, unit arrow.TimeUnit) string {
	var perSecond int64
	var digits int
	switch unit {
	case arrow.Second:
		perSecond, digits = 1, 0
	case arrow.Millisecond:
		perSecond, digits = 1_000, 3
	case arrow.Microsecond:
		perSecond, digits = 1_000_000, 6
	default:
		perSecond, digits = 1_000_000_000, 9
	}
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	seconds, fraction := value/perSecond, value%perSecond
	text := fmt.Sprintf("%s%02d:%02d:%02d", sign, seconds/3600, seconds/60%60, seconds%60)
	if fraction != 0 {
		text += "." + strings.TrimRight(fmt.Sprintf("%0*d", digits, fraction), "0")
	}
	return text
}

// formatInterval3 writes an interval of months, days and microseconds as
// DuckDB does: "1 year 2 months 3 days 04:05:06.5", "-1 day", "00:00:00".
func formatInterval3(months, days, micros int64) string {
	var parts []string
	unit := func(value int64, name string) {
		if value == 0 {
			return
		}
		text := strconv.FormatInt(value, 10) + " " + name
		if value != 1 && value != -1 {
			text += "s"
		}
		parts = append(parts, text)
	}
	unit(months/12, "year")
	unit(months%12, "month")
	unit(days, "day")
	if micros != 0 {
		sign := ""
		magnitude := uint64(micros)
		if micros < 0 {
			sign, magnitude = "-", uint64(-micros)
		}
		seconds, fraction := magnitude/1_000_000, magnitude%1_000_000
		text := fmt.Sprintf("%s%02d:%02d:%02d", sign, seconds/3600, seconds/60%60, seconds%60)
		if fraction != 0 {
			text += "." + strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}

func formatUUID(raw []byte) (string, error) {
	if len(raw) != 16 {
		return "", fmt.Errorf("a UUID of %d bytes, not 16", len(raw))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// decodeHugeint reads DuckDB's HUGEINT or UHUGEINT: 16 bytes, little endian,
// two's complement when signed.
func decodeHugeint(raw []byte, signed bool) (string, error) {
	if len(raw) != 16 {
		return "", fmt.Errorf("a HUGEINT of %d bytes, not 16", len(raw))
	}
	bigEndian := make([]byte, 16)
	for i, b := range raw {
		bigEndian[15-i] = b
	}
	value := new(big.Int).SetBytes(bigEndian)
	if signed && bigEndian[0]&0x80 != 0 {
		value.Sub(value, new(big.Int).Lsh(big.NewInt(1), 128))
	}
	return value.String(), nil
}

// decodeBignum reads DuckDB's BIGNUM: a 3-byte header (the sign bit set for
// a number that is not negative, then the count of data bytes), then the
// magnitude, big endian. A negative number has every byte inverted.
func decodeBignum(raw []byte) (string, error) {
	if len(raw) < 4 {
		return "", fmt.Errorf("a BIGNUM of %d bytes, shorter than its header", len(raw))
	}
	negative := raw[0]&0x80 == 0
	magnitude := make([]byte, len(raw)-3)
	copy(magnitude, raw[3:])
	if negative {
		for i := range magnitude {
			magnitude[i] = ^magnitude[i]
		}
	}
	value := new(big.Int).SetBytes(magnitude)
	if negative {
		value.Neg(value)
	}
	return value.String(), nil
}

// decodeBit reads DuckDB's BIT: one byte counting the padding bits, then the
// bits, padded at the front.
func decodeBit(raw []byte) (string, error) {
	if len(raw) < 1 || raw[0] > 7 {
		return "", errors.New("a BIT value without a valid padding byte")
	}
	padding := int(raw[0])
	var bits strings.Builder
	for index, b := range raw[1:] {
		for bit := 7; bit >= 0; bit-- {
			if index == 0 && 7-bit < padding {
				continue
			}
			if b>>uint(bit)&1 == 1 {
				bits.WriteByte('1')
			} else {
				bits.WriteByte('0')
			}
		}
	}
	return bits.String(), nil
}

// timeTZMaxOffset is the largest UTC offset of a TIMETZ, in seconds; DuckDB
// stores an offset as this minus the offset.
const timeTZMaxOffset = 16*60*60 - 1

// decodeTimeTZ reads DuckDB's TIMETZ: 64 bits, little endian, the
// microseconds of the day above the low 24 bits, which hold the offset.
func decodeTimeTZ(raw []byte) (string, error) {
	if len(raw) != 8 {
		return "", fmt.Errorf("a TIMETZ of %d bytes, not 8", len(raw))
	}
	bits := binary.LittleEndian.Uint64(raw)
	micros := int64(bits >> 24)
	offset := timeTZMaxOffset - int64(bits&0xFFFFFF)
	text := formatTimeOfDay(micros, arrow.Microsecond)
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	text += fmt.Sprintf("%s%02d", sign, offset/3600)
	if minutes, seconds := offset/60%60, offset%60; minutes != 0 || seconds != 0 {
		text += fmt.Sprintf(":%02d", minutes)
		if seconds != 0 {
			text += fmt.Sprintf(":%02d", seconds)
		}
	}
	return text, nil
}

// wkbToWKT writes well-known binary (ISO, or PostGIS's extended form) as
// well-known text, as DuckDB prints GEOMETRY: "POINT (1 2)",
// "LINESTRING Z (1 2 3, 4 5 6)", "POLYGON EMPTY".
func wkbToWKT(raw []byte) (string, error) {
	reader := &wkbReader{raw: raw}
	var out strings.Builder
	if err := reader.geometry(&out, 0); err != nil {
		return "", err
	}
	if reader.pos != len(raw) {
		return "", errors.New("well-known binary with trailing bytes")
	}
	return out.String(), nil
}

type wkbReader struct {
	raw   []byte
	pos   int
	order binary.ByteOrder
}

var wkbNames = map[uint32]string{
	1: "POINT", 2: "LINESTRING", 3: "POLYGON", 4: "MULTIPOINT", 5: "MULTILINESTRING",
	6: "MULTIPOLYGON", 7: "GEOMETRYCOLLECTION",
}

const maxWKBDepth = 32

func (r *wkbReader) uint32() (uint32, error) {
	if r.pos+4 > len(r.raw) {
		return 0, errors.New("well-known binary ends early")
	}
	value := r.order.Uint32(r.raw[r.pos:])
	r.pos += 4
	return value, nil
}

func (r *wkbReader) float64() (float64, error) {
	if r.pos+8 > len(r.raw) {
		return 0, errors.New("well-known binary ends early")
	}
	value := math.Float64frombits(r.order.Uint64(r.raw[r.pos:]))
	r.pos += 8
	return value, nil
}

// header reads a geometry's byte order and type: its base type (1 to 7) and
// how many ordinates its points have beyond x and y.
func (r *wkbReader) header() (kind uint32, hasZ, hasM bool, err error) {
	if r.pos >= len(r.raw) {
		return 0, false, false, errors.New("well-known binary ends early")
	}
	switch r.raw[r.pos] {
	case 0:
		r.order = binary.BigEndian
	case 1:
		r.order = binary.LittleEndian
	default:
		return 0, false, false, errors.New("well-known binary with an unknown byte order")
	}
	r.pos++
	code, err := r.uint32()
	if err != nil {
		return 0, false, false, err
	}
	// PostGIS's flags.
	hasZ, hasM = code&0x80000000 != 0, code&0x40000000 != 0
	hasSRID := code&0x20000000 != 0
	code &= 0x1FFFFFFF
	// ISO's thousands.
	switch code / 1000 {
	case 1:
		hasZ = true
	case 2:
		hasM = true
	case 3:
		hasZ, hasM = true, true
	}
	kind = code % 1000
	if _, known := wkbNames[kind]; !known {
		return 0, false, false, fmt.Errorf("well-known binary with the unknown geometry type %d", code)
	}
	if hasSRID {
		if _, err := r.uint32(); err != nil {
			return 0, false, false, err
		}
	}
	return kind, hasZ, hasM, nil
}

// formatOrdinate writes a coordinate as DuckDB does: the shortest digits
// that read back the same, with an exponent only below 1e-4 and from 1e16.
func formatOrdinate(value float64) string {
	if magnitude := math.Abs(value); magnitude != 0 && (magnitude < 1e-4 || magnitude >= 1e16) {
		return strconv.FormatFloat(value, 'e', -1, 64)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// point writes one point's ordinates; it reports whether they are all NaN
// (an empty point).
func (r *wkbReader) point(out *strings.Builder, ordinates int) (empty bool, err error) {
	empty = true
	for index := 0; index < ordinates; index++ {
		value, err := r.float64()
		if err != nil {
			return false, err
		}
		if !math.IsNaN(value) {
			empty = false
		}
		if index > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(formatOrdinate(value))
	}
	return empty, nil
}

// sequence writes count points as "(x y, x y)".
func (r *wkbReader) sequence(out *strings.Builder, ordinates int) error {
	count, err := r.uint32()
	if err != nil {
		return err
	}
	if uint64(count)*uint64(ordinates)*8 > uint64(len(r.raw)-r.pos) {
		return errors.New("well-known binary ends early")
	}
	out.WriteByte('(')
	for index := uint32(0); index < count; index++ {
		if index > 0 {
			out.WriteString(", ")
		}
		if _, err := r.point(out, ordinates); err != nil {
			return err
		}
	}
	out.WriteByte(')')
	return nil
}

func (r *wkbReader) geometry(out *strings.Builder, depth int) error {
	if depth > maxWKBDepth {
		return errors.New("well-known binary nested too deeply")
	}
	kind, hasZ, hasM, err := r.header()
	if err != nil {
		return err
	}
	out.WriteString(wkbNames[kind])
	ordinates := 2
	switch {
	case hasZ && hasM:
		out.WriteString(" ZM")
		ordinates = 4
	case hasZ:
		out.WriteString(" Z")
		ordinates = 3
	case hasM:
		out.WriteString(" M")
		ordinates = 3
	}
	return r.body(out, kind, ordinates, depth)
}

// body writes what follows a geometry's name: " EMPTY" or " (...)".
func (r *wkbReader) body(out *strings.Builder, kind uint32, ordinates, depth int) error {
	if kind == 1 {
		var point strings.Builder
		empty, err := r.point(&point, ordinates)
		if err != nil {
			return err
		}
		if empty {
			out.WriteString(" EMPTY")
			return nil
		}
		out.WriteString(" (" + point.String() + ")")
		return nil
	}
	if kind == 2 {
		return r.counted(out, func(inner *strings.Builder) error { return r.sequence(inner, ordinates) }, true)
	}
	count, err := r.uint32()
	if err != nil {
		return err
	}
	if uint64(count) > uint64(len(r.raw)-r.pos) {
		return errors.New("well-known binary ends early")
	}
	if count == 0 {
		out.WriteString(" EMPTY")
		return nil
	}
	out.WriteString(" (")
	for index := uint32(0); index < count; index++ {
		if index > 0 {
			out.WriteString(", ")
		}
		switch kind {
		case 3: // a polygon's rings
			if err := r.sequence(out, ordinates); err != nil {
				return err
			}
		case 7: // a collection's geometries, each with its own name
			if err := r.geometry(out, depth+1); err != nil {
				return err
			}
		default: // MULTI*: members of one type, written without their name
			member, _, _, err := r.header()
			if err != nil {
				return err
			}
			if member != kind-3 {
				return fmt.Errorf("a %s holding a %s", wkbNames[kind], wkbNames[member])
			}
			var inner strings.Builder
			if err := r.body(&inner, member, ordinates, depth+1); err != nil {
				return err
			}
			text := strings.TrimPrefix(inner.String(), " ")
			if kind == 4 { // a multipoint's points are written bare: (1 2, 3 4)
				text = strings.TrimSuffix(strings.TrimPrefix(text, "("), ")")
			}
			out.WriteString(text)
		}
	}
	out.WriteByte(')')
	return nil
}

// counted writes a line string: " EMPTY" when it has no points.
func (r *wkbReader) counted(out *strings.Builder, write func(*strings.Builder) error, _ bool) error {
	var inner strings.Builder
	if err := write(&inner); err != nil {
		return err
	}
	if inner.String() == "()" {
		out.WriteString(" EMPTY")
		return nil
	}
	out.WriteString(" " + inner.String())
	return nil
}
