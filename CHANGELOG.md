# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-11-13

The first release: a Grafana data source for [Hardcharger](https://gizmodata.com/hardcharger),
GizmoData's DuckDB 2.0 server, over the Chargewire protocol.

### Added

- **Backend** in Go on the Hardcharger ADBC driver (`hardcharger-adbc/go/v2` 2.5.1), for dashboards,
  Explore, alert rules, annotations and query variables.
- **Connection**: host and port or a `hardcharger://` URI; TLS on by default, with a CA certificate,
  a server name override and a client certificate (mutual TLS); sign-in with a username and
  password, an access token, or the client certificate alone. Every secret is stored encrypted by
  Grafana, and every setting can be changed after the data source is created.
- **One connection handle per data source**, shared by its queries and closed when the settings
  change or the data source is deleted. Each query runs in a session of its own, which starts in
  the data source's default catalog and schema.
- **Cancellation**: when Grafana cancels a request, or a query passes the data source's query
  timeout (300 s unless set), the statement is cancelled on the server.
- **Row cap**: at most "Max rows" rows (100,000 unless set) are read from a query; a longer result
  is cut off with a notice on the panel, and the server stops producing it.
- **Type mapping**: every DuckDB type has a Grafana type chosen for it, a NULL stays a NULL, and a
  type without a mapping is an error that names it. Nested values (LIST, STRUCT, MAP, UNION,
  VARIANT) arrive as JSON text, GEOMETRY as well-known text.
- **Query editor**: SQL with completions for macros and dashboard variables, Table and Time series
  formats (long rows are pivoted to one series per label value), and a cheat sheet in Explore. The
  query runs when the editor loses focus or on Ctrl/Cmd+S.
- **Macros**, expanded on SQL tokens (never inside strings, quoted identifiers or comments):
  `$__timeFilter`, `$__timeFrom`, `$__timeTo`, `$__timeGroup` and `$__timeGroupAlias` with gap
  filling, `$__unixEpochFilter`, `$__unixEpochFrom`, `$__unixEpochTo`, `$__unixEpochGroup`,
  `$__unixEpochGroupAlias`, `$__interval`, `$__interval_ms`, `$__from` and `$__to`.
- **Dashboard variables** are written into the SQL as literals: one value as one literal, several
  as a comma-separated list. Query variables take their options from a SQL query (`__text` and
  `__value` columns for labels that differ from values).
- **Health check** that signs in, runs a query and reports the server's version, catalog and
  schema.
- **Query tags**: each statement carries the Grafana dashboard, panel, alert rule, user and data
  source it came from in the server's query tag (can be turned off).
- **Errors** with the server's own message, reported as the query's (HTTP 400-class, source
  `downstream`) unless the plugin itself failed. Secrets and credentials in URIs never appear in an
  error or a log line.
- A development stack (`docker compose up`): a Hardcharger server with sample data, Grafana with a
  provisioned data source and a sample dashboard.
