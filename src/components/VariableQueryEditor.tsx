import React from 'react';

import { QueryEditorProps } from '@grafana/data';
import { Stack } from '@grafana/ui';

import type { DataSource } from '../datasource';
import { HardchargerOptions, HardchargerQuery, HardchargerVariableQuery } from '../types';
import { SqlEditor } from './SqlEditor';

type Props = QueryEditorProps<DataSource, HardchargerQuery, HardchargerOptions, HardchargerVariableQuery>;

/** A variable saved by an older dashboard or a provisioning file may be plain SQL text. */
export function sqlOf(query: unknown): string {
  if (typeof query === 'string') {
    return query;
  }
  const given = (query ?? {}) as { rawSql?: unknown; query?: unknown };
  return typeof given.rawSql === 'string' ? given.rawSql : typeof given.query === 'string' ? given.query : '';
}

export function VariableQueryEditor({ query, onChange }: Props) {
  const refId = (typeof query === 'object' && query?.refId) || 'variable';
  return (
    <Stack direction="column" gap={1}>
      <SqlEditor
        ariaLabel="Variable SQL"
        height={120}
        value={sqlOf(query)}
        onCommit={(sql) => onChange({ refId, rawSql: sql })}
      />
      <span>
        Every value the query returns becomes an option. For a label that differs from the value, name the columns{' '}
        <code>__text</code> and <code>__value</code>. The query runs when the editor loses focus.
      </span>
    </Stack>
  );
}
