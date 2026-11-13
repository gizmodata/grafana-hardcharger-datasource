import React, { useCallback, useRef } from 'react';

import { getTemplateSrv } from '@grafana/runtime';
import { CodeEditor, CodeEditorSuggestionItem, CodeEditorSuggestionItemKind } from '@grafana/ui';

import { MACROS } from './macros';

interface Props {
  value: string;
  /** The editor lost focus, or Ctrl/Cmd+S: the SQL is final for now. */
  onCommit: (sql: string) => void;
  height?: number;
  ariaLabel?: string;
}

export function suggestions(): CodeEditorSuggestionItem[] {
  const macros: CodeEditorSuggestionItem[] = MACROS.map((macro) => ({
    label: macro.name,
    kind: CodeEditorSuggestionItemKind.Method,
    insertText: macro.insert,
    detail: 'Macro',
    documentation: macro.description,
  }));
  const variables: CodeEditorSuggestionItem[] = getTemplateSrv()
    .getVariables()
    .map((variable) => ({
      label: '$' + variable.name,
      kind: CodeEditorSuggestionItemKind.Text,
      insertText: '${' + variable.name + '}',
      detail: 'Dashboard variable',
      documentation:
        'Written into the SQL as a literal: quoted unless it is a number, a comma-separated list when it has several values.',
    }));
  return [...macros, ...variables];
}

/** The SQL editor of the query editor and the variable editor. */
export function SqlEditor({ value, onCommit, height = 180, ariaLabel = 'SQL' }: Props) {
  // What was last committed: leaving the editor without a change does not run the query again.
  const committed = useRef(value);
  const commit = useCallback(
    (sql: string, force: boolean) => {
      if (force || sql !== committed.current) {
        committed.current = sql;
        onCommit(sql);
      }
    },
    [onCommit]
  );
  return (
    <div aria-label={ariaLabel} data-testid="hardcharger-sql-editor">
      <CodeEditor
        language="sql"
        height={height}
        value={value}
        showMiniMap={false}
        showLineNumbers={true}
        onBlur={(sql) => commit(sql, false)}
        onSave={(sql) => commit(sql, true)}
        getSuggestions={suggestions}
        monacoOptions={{ wordWrap: 'on', scrollBeyondLastLine: false, fontSize: 13 }}
      />
    </div>
  );
}
