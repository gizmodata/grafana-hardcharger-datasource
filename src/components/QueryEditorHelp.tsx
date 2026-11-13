import React from 'react';

import { QueryEditorHelpProps } from '@grafana/data';

import { HardchargerQuery } from '../types';
import { MACROS } from './macros';

const examples: Array<{ title: string; query: HardchargerQuery }> = [
  {
    title: 'A table',
    query: { refId: 'A', format: 'table', rawSql: 'SELECT *\nFROM information_schema.tables\nLIMIT 100' },
  },
  {
    title: 'One series per host, averaged over the panel interval',
    query: {
      refId: 'A',
      format: 'time_series',
      rawSql:
        'SELECT $__timeGroup(ts, $__interval) AS time, host, avg(cpu) AS cpu\nFROM metrics\nWHERE $__timeFilter(ts)\nGROUP BY 1, 2\nORDER BY 1',
    },
  },
  {
    title: 'A count per interval, with empty buckets filled with zero',
    query: {
      refId: 'A',
      format: 'time_series',
      rawSql:
        'SELECT $__timeGroup(ts, $__interval, 0) AS time, count(*) AS events\nFROM events\nWHERE $__timeFilter(ts)\nGROUP BY 1\nORDER BY 1',
    },
  },
];

/** The cheat sheet Explore shows next to the query editor. */
export function QueryEditorHelp({ onClickExample }: QueryEditorHelpProps<HardchargerQuery>) {
  return (
    <div>
      <h3>Hardcharger</h3>
      <p>
        Queries are DuckDB SQL, run on the Hardcharger server. Dashboard variables are written in as literals:{' '}
        <code>WHERE host = $host</code>, <code>WHERE host IN ($hosts)</code>.
      </p>
      <h4>Examples</h4>
      {examples.map((example) => (
        <div key={example.title} style={{ marginBottom: 12 }}>
          <div>{example.title}</div>
          <pre
            role="button"
            tabIndex={0}
            style={{ cursor: 'pointer' }}
            onClick={() => onClickExample(example.query)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                onClickExample(example.query);
              }
            }}
          >
            {example.query.rawSql}
          </pre>
        </div>
      ))}
      <h4>Macros</h4>
      <table>
        <tbody>
          {MACROS.map((macro) => (
            <tr key={macro.name}>
              <td style={{ paddingRight: 16, whiteSpace: 'nowrap', verticalAlign: 'top' }}>
                <code>{macro.name}</code>
              </td>
              <td>{macro.description}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
