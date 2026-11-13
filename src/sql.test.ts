import { FieldType, toDataFrame } from '@grafana/data';

import { framesToMetricFindValues, interpolateVariable, quoteLiteral, sqlLiteral } from './sql';

describe('sqlLiteral', () => {
  it.each([
    ['web-1', "'web-1'"],
    ["O'Brien", "'O''Brien'"],
    ["'; DROP TABLE users; --", "'''; DROP TABLE users; --'"],
    ["a''b", "'a''''b'"],
    ['', "''"],
    [' 1 ', "' 1 '"],
    ['back\\slash', "'back\\slash'"],
    ['line\nbreak', "'line\nbreak'"],
    ['héllo ✓', "'héllo ✓'"],
  ])('writes the text %j as a string literal', (value, want) => {
    expect(sqlLiteral(value)).toBe(want);
  });

  it.each([
    ['0', '0'],
    ['42', '42'],
    ['-7', '-7'],
    ['3.14', '3.14'],
    ['-0.5', '-0.5'],
  ])('writes the plain number %j as it is', (value, want) => {
    expect(sqlLiteral(value)).toBe(want);
  });

  // Anything that is not a plainly written number stays a string: it can never be read as SQL.
  it.each(['007', '1e5', '0x10', 'Infinity', 'NaN', '1.', '.5', '+1', '1 OR 1=1', '1;', '--1', '1,2', '1_000'])(
    'quotes %j, which only looks like a number',
    (value) => {
      expect(sqlLiteral(value)).toBe(quoteLiteral(value));
    }
  );

  it('writes numbers, booleans and nothing', () => {
    expect(sqlLiteral(5)).toBe('5');
    expect(sqlLiteral(-2.5)).toBe('-2.5');
    expect(sqlLiteral(NaN)).toBe('NULL');
    expect(sqlLiteral(Infinity)).toBe('NULL');
    expect(sqlLiteral(true)).toBe('TRUE');
    expect(sqlLiteral(false)).toBe('FALSE');
    expect(sqlLiteral(null)).toBe('NULL');
    expect(sqlLiteral(undefined)).toBe('NULL');
  });
});

describe('interpolateVariable', () => {
  it('writes one value as one literal', () => {
    expect(interpolateVariable('web-1', { name: 'host' })).toBe("'web-1'");
    expect(interpolateVariable('25', { name: 'limit' })).toBe('25');
    expect(interpolateVariable(25, { name: 'limit' })).toBe('25');
  });

  it('writes several values as a list of literals', () => {
    expect(interpolateVariable(['web-1', "it's", '3'], { name: 'host' })).toBe("'web-1','it''s',3");
    expect(interpolateVariable(['only'], { name: 'host' })).toBe("'only'");
  });

  it('writes no values as NULL, so IN () stays valid SQL and matches nothing', () => {
    expect(interpolateVariable([], { name: 'host' })).toBe('NULL');
  });

  it("leaves Grafana's own interval variables as they are", () => {
    expect(interpolateVariable('30s', { name: '__interval' })).toBe('30s');
    expect(interpolateVariable('5m', { name: '__rate_interval' })).toBe('5m');
    expect(interpolateVariable('30000', { name: '__interval_ms' })).toBe('30000');
    expect(interpolateVariable('1794564000000', { name: '__from' })).toBe('1794564000000');
  });

  it("quotes Grafana's other built-in variables", () => {
    expect(interpolateVariable('My dashboard', { name: '__dashboard' })).toBe("'My dashboard'");
    expect(interpolateVariable("o'neil", { name: '__user' })).toBe("'o''neil'");
  });

  it('quotes a dashboard variable even when it holds an interval or has no description', () => {
    expect(interpolateVariable('5m', { name: 'bucket' })).toBe("'5m'");
    expect(interpolateVariable('5m', undefined)).toBe("'5m'");
    expect(interpolateVariable("x' OR '1'='1", undefined)).toBe("'x'' OR ''1''=''1'");
  });
});

describe('framesToMetricFindValues', () => {
  it('makes an option of every value of every column, once', () => {
    const frame = toDataFrame({
      fields: [
        { name: 'host', type: FieldType.string, values: ['web-1', 'web-2', 'web-1', null] },
        { name: 'n', type: FieldType.number, values: [1, 2, 1, 3] },
      ],
    });
    expect(framesToMetricFindValues([frame])).toEqual([
      { text: 'web-1', value: 'web-1' },
      { text: 'web-2', value: 'web-2' },
      { text: '1', value: '1' },
      { text: '2', value: '2' },
      { text: '3', value: '3' },
    ]);
  });

  it('uses __text and __value columns as label and value', () => {
    const frame = toDataFrame({
      fields: [
        { name: '__value', type: FieldType.number, values: [10, 20, 20, null] },
        { name: 'ignored', type: FieldType.string, values: ['a', 'b', 'c', 'd'] },
        { name: '__text', type: FieldType.string, values: ['Ten', 'Twenty', 'Twenty', 'None'] },
      ],
    });
    expect(framesToMetricFindValues([frame])).toEqual([
      { text: 'Ten', value: '10' },
      { text: 'Twenty', value: '20' },
    ]);
  });

  it('uses a lone __value or __text column for both', () => {
    const values = toDataFrame({ fields: [{ name: '__value', type: FieldType.string, values: ['a', 'b'] }] });
    const texts = toDataFrame({ fields: [{ name: '__text', type: FieldType.string, values: ['c'] }] });
    expect(framesToMetricFindValues([values, texts])).toEqual([
      { text: 'a', value: 'a' },
      { text: 'b', value: 'b' },
      { text: 'c', value: 'c' },
    ]);
  });

  it('writes times as ISO 8601 and booleans as text', () => {
    const frame = toDataFrame({
      fields: [
        { name: 'ts', type: FieldType.time, values: [Date.UTC(2026, 0, 2, 3, 4, 5)] },
        { name: 'ok', type: FieldType.boolean, values: [true] },
      ],
    });
    expect(framesToMetricFindValues([frame])).toEqual([
      { text: '2026-01-02T03:04:05.000Z', value: '2026-01-02T03:04:05.000Z' },
      { text: 'true', value: 'true' },
    ]);
  });

  it('gives no options for no rows or no frames', () => {
    expect(framesToMetricFindValues([])).toEqual([]);
    expect(
      framesToMetricFindValues([toDataFrame({ fields: [{ name: 'host', type: FieldType.string, values: [] }] })])
    ).toEqual([]);
  });
});
