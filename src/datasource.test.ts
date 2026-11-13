import { firstValueFrom, of } from 'rxjs';

import {
  CoreApp,
  DataQueryRequest,
  DataQueryResponse,
  DataSourceInstanceSettings,
  FieldType,
  LoadingState,
  toDataFrame,
} from '@grafana/data';

import { DataSource, HardchargerVariableSupport, normalizeVariableQuery } from './datasource';
import { interpolateVariable } from './sql';
import { DEFAULT_QUERY, HardchargerOptions, HardchargerQuery, HardchargerVariableQuery } from './types';

// A stand-in for Grafana's template service: $name and ${name} from a fixed set of variables, each
// passed through the data source's own formatter with the variable it came from.
const variables: Record<string, unknown> = {
  host: 'web-1',
  hosts: ['web-1', "o'neil", '7'],
  none: [],
  limit: '10',
  __interval: '30s',
  __interval_ms: '30000',
};
const replace = jest.fn((text: string, _scoped: unknown, format: (value: unknown, variable: { name: string }) => string) =>
  text.replace(/\$\{(\w+)\}|\$(\w+)/g, (match, braced, bare) => {
    const name = braced ?? bare;
    return name in variables ? format(variables[name], { name }) : match;
  })
);

jest.mock('@grafana/runtime', () => ({
  ...jest.requireActual('@grafana/runtime'),
  getTemplateSrv: () => ({ replace, getVariables: () => [] }),
}));

const settings = {
  id: 1,
  uid: 'hc',
  type: 'gizmodata-hardcharger-datasource',
  name: 'Hardcharger',
  jsonData: {},
  access: 'proxy',
  readOnly: false,
  meta: { id: 'gizmodata-hardcharger-datasource', name: 'Hardcharger' },
} as unknown as DataSourceInstanceSettings<HardchargerOptions>;

function sql(datasource: DataSource, rawSql: string): string {
  return datasource.applyTemplateVariables({ refId: 'A', rawSql, format: 'table' }, {}).rawSql ?? '';
}

describe('DataSource', () => {
  const datasource = new DataSource(settings);

  it('starts a new query from the default', () => {
    expect(datasource.getDefaultQuery(CoreApp.PanelEditor)).toEqual(DEFAULT_QUERY);
  });

  it('does not send a query without SQL, or a hidden one', () => {
    expect(datasource.filterQuery({ refId: 'A', rawSql: 'SELECT 1' })).toBe(true);
    expect(datasource.filterQuery({ refId: 'A', rawSql: '  \n ' })).toBe(false);
    expect(datasource.filterQuery({ refId: 'A' })).toBe(false);
    expect(datasource.filterQuery({ refId: 'A', rawSql: 'SELECT 1', hide: true })).toBe(false);
  });

  it('writes dashboard variables into the SQL as literals', () => {
    expect(sql(datasource, 'SELECT * FROM t WHERE host = $host LIMIT ${limit}')).toBe(
      "SELECT * FROM t WHERE host = 'web-1' LIMIT 10"
    );
    expect(sql(datasource, 'SELECT * FROM t WHERE host IN ($hosts)')).toBe(
      "SELECT * FROM t WHERE host IN ('web-1','o''neil',7)"
    );
    expect(sql(datasource, 'SELECT * FROM t WHERE host IN ($none)')).toBe('SELECT * FROM t WHERE host IN (NULL)');
    expect(replace).toHaveBeenLastCalledWith(expect.any(String), {}, interpolateVariable);
  });

  it("leaves the backend's macros and Grafana's interval for the backend", () => {
    expect(sql(datasource, 'SELECT $__timeGroup(ts, $__interval) FROM t WHERE $__timeFilter(ts)')).toBe(
      'SELECT $__timeGroup(ts, 30s) FROM t WHERE $__timeFilter(ts)'
    );
    expect(sql(datasource, 'SELECT $__interval_ms, $__timeFrom(), $__unknown')).toBe(
      'SELECT 30000, $__timeFrom(), $__unknown'
    );
  });

  it('keeps the rest of the query', () => {
    const query: HardchargerQuery = { refId: 'B', rawSql: 'SELECT 1', format: 'time_series', hide: false };
    expect(datasource.applyTemplateVariables(query, {})).toEqual(query);
    expect(datasource.applyTemplateVariables({ refId: 'C' }, {}).rawSql).toBe('');
  });

  it('offers annotations through the query editor, with an example to start from', () => {
    expect(datasource.annotations).toBeDefined();
    expect(datasource.annotations?.getDefaultQuery?.()).toEqual(
      expect.objectContaining({ format: 'table', rawSql: expect.stringContaining('AS time') })
    );
  });

  it('shows the SQL as the query', () => {
    expect(datasource.getQueryDisplayText({ refId: 'A', rawSql: 'SELECT 1' })).toBe('SELECT 1');
  });
});

describe('normalizeVariableQuery', () => {
  it('reads SQL text, { query } and { rawSql }', () => {
    expect(normalizeVariableQuery('SELECT host FROM t')).toEqual({
      refId: 'variable',
      rawSql: 'SELECT host FROM t',
      format: 'table',
    });
    expect(normalizeVariableQuery({ query: 'SELECT 1', refId: 'X' })).toEqual(
      expect.objectContaining({ refId: 'X', rawSql: 'SELECT 1', format: 'table' })
    );
    expect(normalizeVariableQuery({ rawSql: 'SELECT 2', format: 'time_series' }, 'R')).toEqual({
      refId: 'R',
      rawSql: 'SELECT 2',
      format: 'table',
    });
    expect(normalizeVariableQuery(undefined).rawSql).toBe('');
    expect(normalizeVariableQuery({ rawSql: 5 }).rawSql).toBe('');
  });
});

describe('HardchargerVariableSupport', () => {
  const frame = toDataFrame({
    fields: [
      { name: '__text', type: FieldType.string, values: ['Web one', 'Web two'] },
      { name: '__value', type: FieldType.string, values: ['web-1', 'web-2'] },
    ],
  });

  function run(response: DataQueryResponse, target: unknown) {
    const datasource = new DataSource(settings);
    const query = jest.spyOn(datasource, 'query').mockReturnValue(of(response));
    const support = new HardchargerVariableSupport(datasource);
    const request = { targets: [target], requestId: 'r1' } as unknown as DataQueryRequest<HardchargerVariableQuery>;
    return { result: firstValueFrom(support.query(request)), query };
  }

  it("runs the variable's SQL as a table query and turns the rows into options", async () => {
    const { result, query } = run({ data: [frame], state: LoadingState.Done }, { refId: 'host', rawSql: 'SELECT 1' });
    await expect(result).resolves.toEqual({
      state: LoadingState.Done,
      data: [
        { text: 'Web one', value: 'web-1' },
        { text: 'Web two', value: 'web-2' },
      ],
    });
    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({
        requestId: 'r1',
        targets: [{ refId: 'host', rawSql: 'SELECT 1', format: 'table' }],
      })
    );
  });

  it('runs a variable saved as plain SQL text', async () => {
    const { query } = run({ data: [] }, 'SELECT DISTINCT host FROM t');
    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ targets: [{ refId: 'variable', rawSql: 'SELECT DISTINCT host FROM t', format: 'table' }] })
    );
  });

  it("passes a failed query's error on", async () => {
    const errors = [{ message: 'Parser Error: syntax error', status: 400 }];
    const { result } = run({ data: [], state: LoadingState.Error, errors }, { refId: 'A', rawSql: 'SELEC' });
    await expect(result).resolves.toEqual({ data: [], state: LoadingState.Error, errors });
  });

  it('has an editor of its own', () => {
    expect(new HardchargerVariableSupport(new DataSource(settings)).editor).toBeDefined();
  });
});
