import React from 'react';

import { QueryEditorProps } from '@grafana/data';
import { InlineField, RadioButtonGroup, Stack, TextLink } from '@grafana/ui';

import { DataSource } from '../datasource';
import { DEFAULT_QUERY, HardchargerOptions, HardchargerQuery, QueryFormat } from '../types';
import { SqlEditor } from './SqlEditor';

type Props = QueryEditorProps<DataSource, HardchargerQuery, HardchargerOptions>;

const formats: Array<{ label: string; value: QueryFormat; description: string }> = [
  { label: 'Table', value: 'table', description: 'The rows as they are' },
  {
    label: 'Time series',
    value: 'time_series',
    description: 'A time column, number columns, and text columns that name the series',
  },
];

export function QueryEditor({ query, onChange, onRunQuery }: Props) {
  const format: QueryFormat = query.format ?? 'table';
  const rawSql = query.rawSql ?? DEFAULT_QUERY.rawSql ?? '';

  return (
    <Stack direction="column" gap={1}>
      {/* The query is saved when the editor is left, not on every keystroke: Grafana runs an
          annotation query on each change, and half-typed SQL is not worth a round trip. */}
      <SqlEditor
        value={rawSql}
        onCommit={(sql) => {
          onChange({ ...query, rawSql: sql, format });
          onRunQuery();
        }}
      />
      <Stack direction="row" gap={2} alignItems="center" wrap="wrap">
        <InlineField
          label="Format"
          labelWidth={12}
          tooltip="Table shows the rows as they are. Time series needs a time column and at least one number column; text columns become the series' labels."
        >
          <RadioButtonGroup
            options={formats}
            value={format}
            onChange={(value) => {
              onChange({ ...query, rawSql, format: value });
              onRunQuery();
            }}
          />
        </InlineField>
        <span>
          Runs when the editor loses focus, and on Ctrl/Cmd+S. Macros: <code>$__timeFilter(column)</code>,{' '}
          <code>$__timeGroup(column, $__interval)</code>,{' '}
          <TextLink href="https://github.com/gizmodata/grafana-hardcharger-datasource#macros" external inline>
            all macros
          </TextLink>
          .
        </span>
      </Stack>
    </Stack>
  );
}
