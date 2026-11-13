import React from 'react';
import { fireEvent, render, screen } from '@testing-library/react';

import { QueryEditorProps } from '@grafana/data';

import type { DataSource } from '../datasource';
import { DEFAULT_QUERY, HardchargerOptions, HardchargerQuery, HardchargerVariableQuery } from '../types';
import { MACROS } from './macros';
import { QueryEditor } from './QueryEditor';
import { QueryEditorHelp } from './QueryEditorHelp';
import { suggestions } from './SqlEditor';
import { sqlOf, VariableQueryEditor } from './VariableQueryEditor';

// Monaco does not run in jsdom: a textarea with the code editor's callbacks stands in for it.
jest.mock('@grafana/ui', () => {
  const actual = jest.requireActual('@grafana/ui');
  return {
    ...actual,
    CodeEditor: (props: {
      value: string;
      onChange?: (value: string) => void;
      onBlur?: (value: string) => void;
      onSave?: (value: string) => void;
    }) => (
      <textarea
        aria-label="code"
        defaultValue={props.value}
        onChange={(event) => props.onChange?.(event.target.value)}
        onBlur={(event) => props.onBlur?.(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 's' && event.ctrlKey) {
            props.onSave?.((event.target as HTMLTextAreaElement).value);
          }
        }}
      />
    ),
  };
});

jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  getTemplateSrv: () => ({ getVariables: () => [{ name: 'host' }, { name: 'region' }] }),
}));

type Props = QueryEditorProps<DataSource, HardchargerQuery, HardchargerOptions>;

function renderQueryEditor(query: HardchargerQuery) {
  const onChange = jest.fn();
  const onRunQuery = jest.fn();
  const props = { query, onChange, onRunQuery, datasource: {} as DataSource } as unknown as Props;
  render(<QueryEditor {...props} />);
  return { onChange, onRunQuery, editor: screen.getByLabelText('code') as HTMLTextAreaElement };
}

describe('QueryEditor', () => {
  it('shows the query, as a table by default', () => {
    const { editor } = renderQueryEditor({ refId: 'A', rawSql: 'SELECT 1' });
    expect(editor).toHaveValue('SELECT 1');
    expect(screen.getByLabelText('Table')).toBeChecked();
    expect(screen.getByLabelText('Time series')).not.toBeChecked();
  });

  it('shows the default SQL for a new query', () => {
    const { editor } = renderQueryEditor({ refId: 'A' });
    expect(editor).toHaveValue(DEFAULT_QUERY.rawSql);
  });

  it('saves the query and runs it when the editor loses focus, not on every keystroke', () => {
    const { editor, onChange, onRunQuery } = renderQueryEditor({ refId: 'A', rawSql: 'SELECT 1' });
    fireEvent.change(editor, { target: { value: 'SELECT' } });
    fireEvent.change(editor, { target: { value: 'SELECT 2' } });
    expect(onChange).not.toHaveBeenCalled();
    expect(onRunQuery).not.toHaveBeenCalled();
    fireEvent.blur(editor);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenLastCalledWith({ refId: 'A', rawSql: 'SELECT 2', format: 'table' });
    expect(onRunQuery).toHaveBeenCalledTimes(1);
  });

  it('does not run the query again when focus leaves without a change', () => {
    const { editor, onRunQuery } = renderQueryEditor({ refId: 'A', rawSql: 'SELECT 1' });
    fireEvent.blur(editor);
    expect(onRunQuery).not.toHaveBeenCalled();
    fireEvent.change(editor, { target: { value: 'SELECT 2' } });
    fireEvent.blur(editor);
    fireEvent.blur(editor);
    expect(onRunQuery).toHaveBeenCalledTimes(1);
  });

  it('runs the query on save, changed or not', () => {
    const { editor, onRunQuery } = renderQueryEditor({ refId: 'A', rawSql: 'SELECT 1' });
    fireEvent.keyDown(editor, { key: 's', ctrlKey: true });
    expect(onRunQuery).toHaveBeenCalledTimes(1);
  });

  it('changes the format and runs the query', () => {
    const { onChange, onRunQuery } = renderQueryEditor({ refId: 'A', rawSql: 'SELECT ts, v FROM t' });
    fireEvent.click(screen.getByLabelText('Time series'));
    expect(onChange).toHaveBeenCalledWith({ refId: 'A', rawSql: 'SELECT ts, v FROM t', format: 'time_series' });
    expect(onRunQuery).toHaveBeenCalledTimes(1);
  });

  it('shows a saved time series query as one', () => {
    renderQueryEditor({ refId: 'A', rawSql: 'SELECT 1', format: 'time_series' });
    expect(screen.getByLabelText('Time series')).toBeChecked();
  });
});

describe('VariableQueryEditor', () => {
  type VariableProps = QueryEditorProps<DataSource, HardchargerQuery, HardchargerOptions, HardchargerVariableQuery>;

  function renderVariableEditor(query: unknown) {
    const onChange = jest.fn();
    const props = { query, onChange, onRunQuery: jest.fn(), datasource: {} as DataSource } as unknown as VariableProps;
    render(<VariableQueryEditor {...props} />);
    return { onChange, editor: screen.getByLabelText('code') as HTMLTextAreaElement };
  }

  it('saves the SQL when the editor loses focus', () => {
    const { editor, onChange } = renderVariableEditor({ refId: 'host', rawSql: 'SELECT 1' });
    expect(editor).toHaveValue('SELECT 1');
    fireEvent.change(editor, { target: { value: 'SELECT DISTINCT host FROM t' } });
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.blur(editor);
    expect(onChange).toHaveBeenCalledWith({ refId: 'host', rawSql: 'SELECT DISTINCT host FROM t' });
  });

  it('opens a variable saved as plain SQL text, or as { query }', () => {
    expect(renderVariableEditor('SELECT a').editor).toHaveValue('SELECT a');
    expect(sqlOf({ query: 'SELECT b' })).toBe('SELECT b');
    expect(sqlOf(undefined)).toBe('');
    expect(sqlOf({ rawSql: 3 })).toBe('');
  });

  it('starts a new variable empty', () => {
    const { editor, onChange } = renderVariableEditor(undefined);
    expect(editor).toHaveValue('');
    fireEvent.change(editor, { target: { value: 'SELECT 1' } });
    fireEvent.blur(editor);
    expect(onChange).toHaveBeenCalledWith({ refId: 'variable', rawSql: 'SELECT 1' });
  });
});

describe('completions', () => {
  it('offers every macro and every dashboard variable', () => {
    const offered = suggestions();
    for (const macro of MACROS) {
      expect(offered).toContainEqual(expect.objectContaining({ label: macro.name, insertText: macro.insert }));
    }
    expect(offered).toContainEqual(expect.objectContaining({ label: '$host', insertText: '${host}' }));
    expect(offered).toContainEqual(expect.objectContaining({ label: '$region', insertText: '${region}' }));
    expect(offered).toHaveLength(MACROS.length + 2);
  });

  it('inserts text that is the macro itself', () => {
    for (const macro of MACROS) {
      expect(macro.insert.startsWith('$__')).toBe(true);
      expect(macro.name.startsWith(macro.insert.split('(')[0])).toBe(true);
    }
  });
});

describe('QueryEditorHelp', () => {
  it('lists the macros and loads an example when it is clicked', () => {
    const onClickExample = jest.fn();
    render(<QueryEditorHelp datasource={{} as never} query={{ refId: 'A' }} onClickExample={onClickExample} />);
    for (const macro of MACROS) {
      expect(screen.getByText(macro.name)).toBeInTheDocument();
    }
    const examples = screen.getAllByRole('button');
    expect(examples.length).toBeGreaterThanOrEqual(3);
    fireEvent.click(examples[1]);
    expect(onClickExample).toHaveBeenCalledWith(
      expect.objectContaining({
        format: 'time_series',
        rawSql: expect.stringContaining('$__timeGroup(ts, $__interval)'),
      })
    );
  });
});
