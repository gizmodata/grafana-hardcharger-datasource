# grafana-hardcharger-datasource

The Grafana data source plugin for Hardcharger, GizmoData's DuckDB 2.0 server (Chargewire protocol).
Plugin id `gizmodata-hardcharger-datasource`, executable `gpx_hardcharger_datasource`.

- **Backend** (`pkg/`): Go, on the Hardcharger ADBC driver (`github.com/gizmodata/hardcharger-adbc/go/v2`).
- **Frontend** (`src/`): TypeScript and React, built by Grafana's scaffold (`.config/`, from
  `@grafana/create-plugin`; do not edit files in `.config/`, extend them from the root configs).
- **Development stack** (`docker-compose.yaml`): a Hardcharger server with sample data
  (`dev/hardcharger/init.sql`) and Grafana with a provisioned data source and dashboard (`provisioning/`).

## Commands

```bash
# Frontend
npm ci
npm run typecheck          # tsc over src/ and tests/
npm run lint
npm run test:ci            # jest; fails when no test runs
npm run build              # dist/

# Backend
gofmt -l pkg               # must print nothing
go vet ./pkg/...
export HARDCHARGER_SERVER_BINARY="$(scripts/hardcharger-server.sh)"   # downloads the server once
scripts/go-test.sh         # unit + live tests; fails if a live test skips or too few ran
scripts/go-test.sh -race
go test ./pkg/... -skip '^TestLive'    # unit tests only, no server
npm run build:backend      # mage buildAll: every platform, CGO_ENABLED=0, into dist/

# End to end (real Grafana + real Hardcharger, in Docker)
docker compose up -d --build           # GRAFANA_PORT / HARDCHARGER_PORT move the ports
npx playwright install chromium
npm run e2e                            # GRAFANA_URL=http://localhost:3000 unless set
npm run screenshots                    # re-captures src/img/screenshot-*.png from the stack
docker compose down

# Release artifacts
scripts/check-version.sh [vX.Y.Z]      # package.json, plugin.json, CHANGELOG (and the tag) agree
scripts/package.sh                     # dist/ -> gizmodata-hardcharger-datasource-X.Y.Z.zip + .sha1
npx -y @grafana/plugin-validator@latest -sourceCodeUri file://./ gizmodata-hardcharger-datasource-X.Y.Z.zip
actionlint                             # after touching .github/workflows
```

## Where things are

| | |
| --- | --- |
| `pkg/models/settings.go` | The settings, their validation, and the driver options they become. Every option key is the driver's: `TestDriverAcceptsEveryOption` fails on a misspelt one. |
| `pkg/plugin/datasource.go` | One ADBC database handle per instance; a session per query; health check; query tags; `Dispose`. |
| `pkg/plugin/macros.go` | Macro expansion on SQL tokens (strings, quoted identifiers, comments and dollar quotes are skipped). |
| `pkg/plugin/convert.go`, `json.go`, `duckdb.go` | Arrow to Grafana: one mapping per type; nested values as JSON; DuckDB's text forms. |
| `pkg/plugin/timeseries.go` | The Time series format: long to wide with the SDK, gap filling. |
| `pkg/plugin/errors.go` | Status and error source of every failure; redaction of secrets. |
| `pkg/plugin/server_test.go` | Starts real `hardcharger-server` processes for the `TestLive*` tests. |
| `src/sql.ts` | How dashboard variables become SQL literals; variable query rows to options. |
| `src/components/` | Config editor, query editor, variable editor, cheat sheet. |
| `tests/` | Playwright end-to-end tests (`@grafana/plugin-e2e`); `fixtures.ts` fails a test on any browser error. |

## Rules of this repository

- **The settings have two readers.** `src/types.ts` (`HardchargerOptions`) and
  `pkg/models/settings.go` (`Settings`) name the same `jsonData` keys: change both, and the
  provisioning table in the README.
- **Never invent a driver option.** Read the option names from the driver's source
  (`hardcharger-adbc/go/hardcharger`, `chargewire-adbc/go/chargewire/options.go`).
- **A new Arrow type gets a deliberate mapping** in `newColumn` and a row in the README's type
  table, and a case in `duckDBTypes` (live test: nullable, NOT NULL, no rows). The default is an
  error that names the type.
- **Text forms are DuckDB's.** `TestLiveTextMatchesTheServer` compares what the plugin shows with the
  server's own `CAST(... AS VARCHAR)`.
- **Live tests fail without a server; nothing skips.** They start their own servers on free ports of
  127.0.0.1 and stop them when the test process ends.
- **Errors**: the SQL's, a macro's or a setting's are 400-class with source `downstream`
  (`userError`); 500 with source `plugin` is for faults of the plugin only. No error or log line may
  hold a secret: build messages through `redactor`.
- **README examples are run before they ship**: the SQL through the e2e or live tests, the
  provisioning and install blocks against real containers.
- **Versions**: `package.json` is the version; `src/plugin.json` keeps `%VERSION%` (the build fills
  it in). A release is a `## [X.Y.Z] - YYYY-MM-DD` section in `CHANGELOG.md` and a `vX.Y.Z` tag;
  `release.yml` runs the whole CI on the tag before it packages anything.
- **Toolchain versions below the latest**: TypeScript 5.9 and ESLint 9 are what
  `@grafana/create-plugin` 9.0.3 scaffolds and tests together (`.config/` depends on them); they
  move with the scaffold, not on their own.
