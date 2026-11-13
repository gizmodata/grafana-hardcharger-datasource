import { DataFrame, FieldType, MetricFindValue } from '@grafana/data';

/** Whole or decimal numbers, written plainly: no exponent, no hex, no leading zeros, no spaces. */
const NUMBER = /^-?(?:0|[1-9]\d*)(?:\.\d+)?$/;

/** What Grafana's own interval variables hold: 30s, 5m, 1h. */
const DURATION = /^\d+(?:\.\d+)?(?:ms|s|m|h|d|w|M|y)$/;

/** A SQL string literal: the text in single quotes, its own quotes doubled. */
export function quoteLiteral(text: string): string {
  return "'" + text.replace(/'/g, "''") + "'";
}

/**
 * One value as a SQL literal: a plain number as it is, anything else as a string literal. Whatever a
 * variable holds, it can only ever be a value in the statement, never SQL.
 */
export function sqlLiteral(value: unknown): string {
  if (value === null || value === undefined) {
    return 'NULL';
  }
  if (typeof value === 'number') {
    return Number.isFinite(value) ? String(value) : 'NULL';
  }
  if (typeof value === 'boolean') {
    return value ? 'TRUE' : 'FALSE';
  }
  const text = String(value);
  return NUMBER.test(text) ? text : quoteLiteral(text);
}

/**
 * How a dashboard variable is written into SQL. A single value becomes one literal (host = $host),
 * several values a comma-separated list of literals (host IN ($hosts)), none NULL.
 *
 * Grafana's own interval variables ($__interval) stay as they are, so $__timeGroup(ts, $__interval)
 * reads the same from a dashboard and from an alert rule. ${name:raw} writes any variable unquoted.
 */
export function interpolateVariable(value: unknown, variable?: { name?: string }): string {
  if (Array.isArray(value)) {
    return value.length > 0 ? value.map(sqlLiteral).join(',') : 'NULL';
  }
  if (typeof value === 'string' && variable?.name?.startsWith('__') && DURATION.test(value)) {
    return value;
  }
  return sqlLiteral(value);
}

function cellText(value: unknown, type: FieldType): string | undefined {
  if (value === null || value === undefined) {
    return undefined;
  }
  if (type === FieldType.time && typeof value === 'number') {
    return new Date(value).toISOString();
  }
  return String(value);
}

/**
 * A variable query's rows as the variable's options. Columns named __text and __value give each
 * option's label and value; without them every value of every column is an option. NULLs are left
 * out, and an option appears once.
 */
export function framesToMetricFindValues(frames: DataFrame[]): MetricFindValue[] {
  const options: MetricFindValue[] = [];
  const seen = new Set<string>();
  const add = (text: string | undefined, value: string | undefined) => {
    if (text === undefined || value === undefined) {
      return;
    }
    const key = JSON.stringify([text, value]);
    if (seen.has(key)) {
      return;
    }
    seen.add(key);
    options.push({ text, value });
  };
  for (const frame of frames) {
    const textField = frame.fields.find((field) => field.name === '__text');
    const valueField = frame.fields.find((field) => field.name === '__value');
    if (textField || valueField) {
      const labels = textField ?? valueField!;
      const values = valueField ?? textField!;
      for (let row = 0; row < frame.length; row++) {
        add(cellText(labels.values[row], labels.type), cellText(values.values[row], values.type));
      }
      continue;
    }
    for (const field of frame.fields) {
      for (let row = 0; row < frame.length; row++) {
        const text = cellText(field.values[row], field.type);
        add(text, text);
      }
    }
  }
  return options;
}
