/** The macros the backend expands, for the editor's completions and its help. */
export interface Macro {
  name: string;
  /** What a completion inserts. */
  insert: string;
  description: string;
}

export const MACROS: Macro[] = [
  {
    name: '$__timeFilter(column)',
    insert: '$__timeFilter(column)',
    description: "The dashboard's time range as a condition: column >= from AND column <= to",
  },
  {
    name: '$__timeFrom()',
    insert: '$__timeFrom()',
    description: "The start of the time range, e.g. '2026-01-31T10:00:00.000Z'",
  },
  { name: '$__timeTo()', insert: '$__timeTo()', description: 'The end of the time range' },
  {
    name: '$__timeGroup(column, interval[, fill])',
    insert: '$__timeGroup(column, $__interval)',
    description:
      'Buckets of time: time_bucket(INTERVAL ..., column). The interval is 30s, 5m, 1h, 1d, 1w, 1M, 1y or $__interval. An optional fill (NULL, previous or a number) fills the buckets without rows in the Time series format.',
  },
  {
    name: '$__timeGroupAlias(column, interval[, fill])',
    insert: '$__timeGroupAlias(column, $__interval)',
    description: '$__timeGroup, named "time"',
  },
  {
    name: '$__unixEpochFilter(column)',
    insert: '$__unixEpochFilter(column)',
    description: 'The time range as a condition on a column of Unix seconds',
  },
  {
    name: '$__unixEpochFrom()',
    insert: '$__unixEpochFrom()',
    description: 'The start of the time range in Unix seconds',
  },
  { name: '$__unixEpochTo()', insert: '$__unixEpochTo()', description: 'The end of the time range in Unix seconds' },
  {
    name: '$__unixEpochGroup(column, interval[, fill])',
    insert: '$__unixEpochGroup(column, $__interval)',
    description: 'Buckets of time over a column of Unix seconds',
  },
  {
    name: '$__unixEpochGroupAlias(column, interval[, fill])',
    insert: '$__unixEpochGroupAlias(column, $__interval)',
    description: '$__unixEpochGroup, named "time"',
  },
  { name: '$__interval', insert: '$__interval', description: "The panel's interval, e.g. 30s" },
  { name: '$__interval_ms', insert: '$__interval_ms', description: "The panel's interval in milliseconds" },
];
