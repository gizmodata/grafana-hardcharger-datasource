import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type QueryFormat = 'table' | 'time_series';

/** A query as the query editor saves it and the backend reads it. */
export interface HardchargerQuery extends DataQuery {
  rawSql?: string;
  format?: QueryFormat;
}

export const DEFAULT_QUERY: Partial<HardchargerQuery> = {
  rawSql: 'SELECT version() AS duckdb_version',
  format: 'table',
};

/** A query variable's query: SQL whose rows become the variable's options. */
export interface HardchargerVariableQuery extends DataQuery {
  rawSql?: string;
}

export type AuthType = 'password' | 'token' | 'client_certificate';

/** The data source's settings (jsonData). The backend's models.Settings reads the same keys. */
export interface HardchargerOptions extends DataSourceJsonData {
  /** A whole hardcharger:// URI; when set, host, port and useTLS are ignored. */
  uri?: string;
  host?: string;
  /** Empty: 31337, or 443 for a GizmoData Cloud host. */
  port?: number;
  /** TLS is on unless this is false. */
  useTLS?: boolean;
  tlsSkipVerify?: boolean;
  tlsServerName?: string;
  /** Default: password. */
  authType?: AuthType;
  username?: string;
  catalog?: string;
  schema?: string;
  /** Empty: 100,000. */
  maxRows?: number;
  /** Seconds. Empty: 300; 0: no limit. */
  queryTimeout?: number;
  /** Statements are tagged with Grafana's context unless this is false. */
  tagQueries?: boolean;
}

/** Secrets: sent to the backend only, never back to the browser. */
export interface HardchargerSecureJsonData {
  password?: string;
  token?: string;
  tlsCACert?: string;
  tlsClientCert?: string;
  tlsClientKey?: string;
}

export const DEFAULT_PORT = 31337;
export const DEFAULT_MAX_ROWS = 100000;
export const DEFAULT_QUERY_TIMEOUT = 300;
