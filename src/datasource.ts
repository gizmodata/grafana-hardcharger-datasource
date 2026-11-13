import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';

import {
  CoreApp,
  CustomVariableSupport,
  DataFrame,
  DataQueryRequest,
  DataQueryResponse,
  DataSourceInstanceSettings,
  ScopedVars,
} from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';

import { VariableQueryEditor } from './components/VariableQueryEditor';
import { framesToMetricFindValues, interpolateVariable } from './sql';
import { DEFAULT_QUERY, HardchargerOptions, HardchargerQuery, HardchargerVariableQuery } from './types';

/**
 * A variable's query as a query: older dashboards and provisioning files give it as plain SQL text
 * or as { query: "…" }.
 */
export function normalizeVariableQuery(query: unknown, refId = 'variable'): HardchargerQuery {
  if (typeof query === 'string') {
    return { refId, rawSql: query, format: 'table' };
  }
  const given = (query ?? {}) as { rawSql?: unknown; query?: unknown; refId?: string };
  const rawSql = typeof given.rawSql === 'string' ? given.rawSql : typeof given.query === 'string' ? given.query : '';
  return { ...(given as object), refId: given.refId ?? refId, rawSql, format: 'table' };
}

/**
 * Query variables: the query is SQL, written in its own editor, run through the backend like any
 * other, and its rows become the variable's options.
 */
export class HardchargerVariableSupport extends CustomVariableSupport<
  DataSource,
  HardchargerVariableQuery,
  HardchargerQuery,
  HardchargerOptions
> {
  editor = VariableQueryEditor;

  constructor(private readonly datasource: DataSource) {
    super();
  }

  query(request: DataQueryRequest<HardchargerVariableQuery>): Observable<DataQueryResponse> {
    const targets = request.targets.map((target) => normalizeVariableQuery(target, target?.refId));
    return this.datasource
      .query({ ...request, targets })
      .pipe(map((response) => ({ ...response, data: framesToMetricFindValues(response.data as DataFrame[]) })));
  }
}

export class DataSource extends DataSourceWithBackend<HardchargerQuery, HardchargerOptions> {
  constructor(instanceSettings: DataSourceInstanceSettings<HardchargerOptions>) {
    super(instanceSettings);
    this.variables = new HardchargerVariableSupport(this);
    // Annotation queries use the query editor; the rows' time, timeEnd, title, text and tags columns
    // become the annotations.
    this.annotations = {
      getDefaultQuery: () => ({
        rawSql:
          '-- Columns named time (and timeEnd), title, text and tags become the annotation.\n' +
          "SELECT now() AS time, 'An example event' AS text, 'example' AS tags",
        format: 'table',
      }),
    };
  }

  getDefaultQuery(_: CoreApp): Partial<HardchargerQuery> {
    return DEFAULT_QUERY;
  }

  /** Dashboard variables are written into the SQL as literals before it goes to the backend. */
  applyTemplateVariables(query: HardchargerQuery, scopedVars: ScopedVars): HardchargerQuery {
    return {
      ...query,
      rawSql: getTemplateSrv().replace(query.rawSql ?? '', scopedVars, interpolateVariable),
    };
  }

  /** A query without SQL is not sent. */
  filterQuery(query: HardchargerQuery): boolean {
    return !query.hide && (query.rawSql ?? '').trim() !== '';
  }

  getQueryDisplayText(query: HardchargerQuery): string {
    return query.rawSql ?? '';
  }
}
