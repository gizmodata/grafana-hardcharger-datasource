// Package plugin is the Hardcharger data source's backend: it runs Grafana's
// queries on a Hardcharger server through the Hardcharger ADBC driver.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/gizmodata/hardcharger-adbc/go/v2/hardcharger"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"

	"github.com/gizmodata/grafana-hardcharger-datasource/pkg/models"
)

// PluginID is the plugin's id, as in plugin.json.
const PluginID = "gizmodata-hardcharger-datasource"

var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
)

const (
	// maxConcurrentQueries is how many queries of one request run at once.
	maxConcurrentQueries = 4
	// closeTimeout bounds closing a session or the database.
	closeTimeout = 10 * time.Second
	// disposeGrace is how long Dispose waits for the queries in flight.
	disposeGrace = 60 * time.Second
	// healthTimeout bounds the health check.
	healthTimeout = 30 * time.Second

	formatTable      = "table"
	formatTimeSeries = "time_series"
)

// serverTimeoutMargin is how long after the plugin's own time limit the
// server's statement timeout is set.
var serverTimeoutMargin = 5 * time.Second

// Datasource is one configured data source: one ADBC database handle, which
// holds the connection pool, the TLS configuration and the sign-in for every
// query of this instance. Grafana makes a new instance when the settings
// change, and disposes of the old one.
type Datasource struct {
	settings *models.Settings
	// settingsErr is why the settings cannot be used; every query and the
	// health check then report it.
	settingsErr error
	db          adbc.DatabaseWithContext
	redactor    redactor
	uid         string
	name        string

	// gate lets Dispose wait for the queries in flight: each holds it for
	// reading, and Dispose takes it for writing before it closes the handle.
	gate      sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

// NewDatasource makes a data source instance. It never fails: unusable
// settings are reported by the health check and by each query, as an error
// of the configuration rather than of the plugin.
func NewDatasource(ctx context.Context, source backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	datasource := &Datasource{uid: source.UID, name: source.Name}
	settings, err := models.LoadSettings(source)
	if err != nil {
		datasource.settingsErr = err
		return datasource, nil
	}
	datasource.settings = settings
	datasource.redactor = redactor{secrets: settings.Secrets()}
	// Nothing is sent to the server here: the driver connects when the first
	// query opens a session.
	driver := hardcharger.NewDriver(memory.DefaultAllocator)
	db, err := driver.NewDatabaseWithContext(ctx, settings.DriverOptions())
	if err != nil {
		datasource.settingsErr = errors.New(datasource.redactor.classify(ctx, err, "").Message)
		return datasource, nil
	}
	datasource.db = db
	return datasource, nil
}

// Dispose closes the database handle: Grafana calls it when the settings
// changed (a new instance serves from then on) or the data source is gone.
func (d *Datasource) Dispose() {
	d.closeOnce.Do(func() {
		if d.db == nil {
			return
		}
		// Queries that already hold this instance finish first, within reason.
		deadline := time.Now().Add(disposeGrace)
		locked := d.gate.TryLock()
		for !locked && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			locked = d.gate.TryLock()
		}
		if locked {
			d.closed = true
			defer d.gate.Unlock()
		}
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := d.db.Close(ctx); err != nil {
			log.DefaultLogger.Warn("closing the Hardcharger database handle failed",
				"datasourceUid", d.uid, "error", d.redactor.redact(err.Error()))
		}
	})
}

// queryModel is a query as the query editor saves it.
type queryModel struct {
	RawSQL string `json:"rawSql"`
	Format string `json:"format"`
}

// QueryData runs a request's queries, a few at a time, each in its own
// session.
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	response := backend.NewQueryDataResponse()
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		slots = make(chan struct{}, maxConcurrentQueries)
	)
	for _, query := range req.Queries {
		wg.Add(1)
		go func(query backend.DataQuery) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			result := d.query(ctx, req, query)
			mu.Lock()
			response.Responses[query.RefID] = result
			mu.Unlock()
		}(query)
	}
	wg.Wait()
	return response, nil
}

func (d *Datasource) query(ctx context.Context, req *backend.QueryDataRequest, query backend.DataQuery) (response backend.DataResponse) {
	logger := log.DefaultLogger.FromContext(ctx)
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("a query panicked", "refId", query.RefID, "panic", d.redactor.redact(fmt.Sprint(recovered)),
				"stack", string(debug.Stack()))
			response = (&queryError{Status: backend.StatusInternal, Source: backend.ErrorSourcePlugin,
				Message: "the Hardcharger data source failed unexpectedly; see Grafana's log"}).response()
		}
	}()
	if d.settingsErr != nil {
		return userError("the data source is not configured correctly: %s", d.settingsErr).response()
	}
	d.gate.RLock()
	defer d.gate.RUnlock()
	if d.closed {
		return (&queryError{Status: backend.StatusInternal, Source: backend.ErrorSourcePlugin,
			Message: "this data source instance was replaced by newer settings; run the query again"}).response()
	}
	var model queryModel
	if err := json.Unmarshal(query.JSON, &model); err != nil {
		return userError("the query could not be read: %s", err).response()
	}
	format := strings.ToLower(strings.TrimSpace(model.Format))
	switch format {
	case "":
		format = formatTable
	case formatTable, formatTimeSeries:
	default:
		return userError("unknown format %q: expected %s or %s", model.Format, formatTable, formatTimeSeries).response()
	}
	if strings.TrimSpace(model.RawSQL) == "" {
		return userError("the query is empty").response()
	}

	sql, fill, err := expandMacros(model.RawSQL, macroParams{
		From:     query.TimeRange.From,
		To:       query.TimeRange.To,
		Interval: queryInterval(query),
	})
	if err != nil {
		return d.redactor.classify(ctx, err, "").response()
	}

	started := time.Now()
	result, err := d.run(ctx, sql, d.queryTag(req, query))
	if err != nil {
		failure := d.redactor.classify(ctx, err, d.timeoutMessage())
		if failure.Source == backend.ErrorSourcePlugin {
			logger.Error("a query failed in the plugin", "refId", query.RefID, "status", int(failure.Status),
				"error", failure.Message)
		} else {
			logger.Debug("a query failed", "refId", query.RefID, "status", int(failure.Status), "error", failure.Message)
		}
		return failure.response()
	}
	frame := result.Frame
	frame.RefID = query.RefID
	frame.Meta = &data.FrameMeta{ExecutedQueryString: sql, PreferredVisualization: data.VisTypeTable}
	if result.Truncated {
		frame.AppendNotices(data.Notice{
			Severity: data.NoticeSeverityWarning,
			Text: fmt.Sprintf("The result was cut off at %d rows, the data source's \"Max rows\" setting. "+
				"Narrow the query (a shorter time range, a WHERE clause, an aggregation or a LIMIT), or raise the setting.",
				d.settings.RowLimit()),
		})
	}
	if format == formatTimeSeries {
		frame, err = toTimeSeries(frame, fill, query.TimeRange, d.settings.RowLimit())
		if err != nil {
			return d.redactor.classify(ctx, err, "").response()
		}
	}
	logger.Debug("a query finished", "refId", query.RefID, "rows", result.Rows, "truncated", result.Truncated,
		"duration", time.Since(started).String())
	return backend.DataResponse{Frames: data.Frames{frame}, Status: backend.StatusOK}
}

// queryInterval is the interval $__interval expands to: Grafana's, or one
// worked out from the time range and the points wanted.
func queryInterval(query backend.DataQuery) time.Duration {
	if query.Interval > 0 {
		return query.Interval
	}
	if query.MaxDataPoints > 0 {
		if span := query.TimeRange.To.Sub(query.TimeRange.From); span > 0 {
			return max((span / time.Duration(query.MaxDataPoints)).Round(time.Millisecond), time.Millisecond)
		}
	}
	return time.Second
}

func (d *Datasource) timeoutMessage() string {
	return fmt.Sprintf("the query ran longer than the data source's query timeout (%s) and was cancelled on the server",
		d.settings.Timeout())
}

// queryTag is the JSON object recorded with a statement on the server: which
// Grafana dashboard, panel, alert rule and user it came from.
func (d *Datasource) queryTag(req *backend.QueryDataRequest, query backend.DataQuery) string {
	if !d.settings.TagsQueries() {
		return ""
	}
	tag := map[string]any{
		"app":            "grafana",
		"plugin":         PluginID,
		"datasource_uid": d.uid,
		"ref_id":         clip(query.RefID),
	}
	if req.PluginContext.OrgID != 0 {
		tag["org_id"] = req.PluginContext.OrgID
	}
	if user := req.PluginContext.User; user != nil && user.Login != "" {
		tag["user"] = clip(user.Login)
	}
	for key, header := range map[string]string{
		"dashboard_uid": "X-Dashboard-Uid",
		"panel_id":      "X-Panel-Id",
		"rule_uid":      "X-Rule-Uid",
	} {
		if value := req.GetHTTPHeader(header); value != "" {
			tag[key] = clip(value)
		}
	}
	if req.Headers[backend.FromAlertHeaderName] == "true" {
		tag["from_alert"] = true
	}
	encoded, err := json.Marshal(tag)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// clip bounds one value of a query tag, which may be 4,096 bytes in all.
func clip(value string) string {
	const limit = 200
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

// queryResult is a statement's rows as a Grafana frame.
type queryResult struct {
	Frame     *data.Frame
	Rows      int64
	Truncated bool
}

// run executes one statement in a session of its own and reads its rows, up
// to the row limit. The session starts in the configured catalog and schema.
// When ctx ends (Grafana cancelled the request) or the query's time limit
// passes, the driver cancels the statement on the server.
func (d *Datasource) run(parent context.Context, sql, tag string) (*queryResult, error) {
	ctx, timeout := parent, d.settings.Timeout()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	result, err := d.runStatement(ctx, sql, tag, timeout)
	if err != nil && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// The query's own time limit, not Grafana's: say which.
		return nil, &queryError{Status: statusRequestTimeout, Source: backend.ErrorSourceDownstream,
			Message: d.timeoutMessage()}
	}
	return result, err
}

func (d *Datasource) runStatement(ctx context.Context, sql, tag string, timeout time.Duration) (*queryResult, error) {
	cnxn, err := d.open(ctx)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(cnxn.Close)

	stmt, err := cnxn.NewStatement(ctx)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(stmt.Close)
	if timeout > 0 {
		// The server's own limit, a little after the plugin's: should the
		// plugin die mid-query, the statement still ends.
		seconds := (timeout + serverTimeoutMargin).Seconds()
		if err := stmt.SetOption(ctx, models.OptionStatementTimeout, fmt.Sprintf("%.3f", seconds)); err != nil {
			return nil, pluginFault("setting the statement timeout", err)
		}
	}
	if tag != "" {
		if err := stmt.SetOption(ctx, models.OptionQueryTag, tag); err != nil {
			return nil, pluginFault("setting the query tag", err)
		}
	}
	if err := stmt.SetSqlQuery(ctx, sql); err != nil {
		return nil, err
	}
	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return nil, err
	}
	// Releasing a reader that still has rows cancels the statement on the server.
	defer reader.Release()
	return readFrame(reader, d.settings.RowLimit())
}

// readFrame reads a result up to limit rows, never more: it stops at the
// first row past the limit.
func readFrame(reader array.RecordReader, limit int64) (*queryResult, error) {
	builder, err := newFrameBuilder(reader.Schema(), limit)
	if err != nil {
		return nil, err
	}
	for reader.Next() {
		more, err := builder.Append(reader.RecordBatch())
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	if !builder.Truncated {
		if err := reader.Err(); err != nil {
			return nil, err
		}
	}
	return &queryResult{Frame: builder.Frame(""), Rows: builder.Rows(), Truncated: builder.Truncated}, nil
}

// pluginFault marks a failure that is the plugin's own doing, whatever
// status the driver gave it.
func pluginFault(doing string, err error) error {
	return &queryError{Status: backend.StatusInternal, Source: backend.ErrorSourcePlugin,
		Message: fmt.Sprintf("%s: %v", doing, err)}
}

// open opens a session in the configured catalog and schema: the catalog
// first, since the schema is looked up in it.
func (d *Datasource) open(ctx context.Context) (adbc.ConnectionWithContext, error) {
	cnxn, err := d.db.Open(ctx)
	if err != nil {
		return nil, err
	}
	setter, ok := cnxn.(adbc.GetSetOptionsWithContext)
	if !ok && (d.settings.Catalog != "" || d.settings.Schema != "") {
		closeQuietly(cnxn.Close)
		return nil, pluginFault("setting the catalog and schema", errors.New("the driver's connection takes no options"))
	}
	if d.settings.Catalog != "" {
		if err := setter.SetOption(ctx, adbc.OptionKeyCurrentCatalog, d.settings.Catalog); err != nil {
			closeQuietly(cnxn.Close)
			return nil, settingError(ctx, "catalog", d.settings.Catalog, err)
		}
	}
	if d.settings.Schema != "" {
		if err := setter.SetOption(ctx, adbc.OptionKeyCurrentDbSchema, d.settings.Schema); err != nil {
			closeQuietly(cnxn.Close)
			return nil, settingError(ctx, "schema", d.settings.Schema, err)
		}
	}
	return cnxn, nil
}

// settingError explains a default catalog or schema the server refused,
// which is the configuration's to fix. Other failures pass through.
func settingError(ctx context.Context, what, value string, err error) error {
	var adbcErr adbc.Error
	if ctx.Err() == nil && errors.As(err, &adbcErr) &&
		(adbcErr.Code == adbc.StatusNotFound || adbcErr.Code == adbc.StatusInvalidArgument) {
		return userError("the data source's default %s %q could not be used: %s", what, value,
			driverPrefix.ReplaceAllString(adbcErr.Msg, ""))
	}
	return err
}

// closeQuietly closes a connection or statement with a time limit of its
// own: the request's context may already be over.
func closeQuietly(closeFn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	_ = closeFn(ctx)
}

// CheckHealth signs in, runs a query and reports the server's version.
func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	if d.settingsErr != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: d.settingsErr.Error()}, nil
	}
	d.gate.RLock()
	defer d.gate.RUnlock()
	if d.closed {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError,
			Message: "this data source instance was replaced by newer settings; test again"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	health, err := d.health(ctx)
	if err != nil {
		failure := d.redactor.classify(ctx, err, fmt.Sprintf("the server did not answer within %s", healthTimeout))
		message := failure.Message
		switch failure.Status {
		case backend.StatusUnauthorized:
			message = "The server refused the credentials: " + message
		case backend.StatusBadGateway:
			message = "The server could not be reached: " + message
		}
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: message}, nil
	}
	details, _ := json.Marshal(health)
	return &backend.CheckHealthResult{
		Status: backend.HealthStatusOk,
		Message: fmt.Sprintf("Connected to %s %s. Catalog: %s, schema: %s.", health.Server, health.Version,
			health.Catalog, health.Schema),
		JSONDetails: details,
	}, nil
}

// healthInfo is what the health check learned.
type healthInfo struct {
	Server  string `json:"server"`
	Version string `json:"version"`
	DuckDB  string `json:"duckdb"`
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
}

const healthQuery = "SELECT version() AS duckdb, current_database() AS catalog, current_schema() AS schema"

func (d *Datasource) health(ctx context.Context) (*healthInfo, error) {
	cnxn, err := d.open(ctx)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(cnxn.Close)
	info := &healthInfo{Server: "Hardcharger"}
	if err := readServerInfo(ctx, cnxn, info); err != nil {
		return nil, err
	}

	stmt, err := cnxn.NewStatement(ctx)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(stmt.Close)
	if d.settings.TagsQueries() {
		tag, _ := json.Marshal(map[string]any{"app": "grafana", "plugin": PluginID, "datasource_uid": d.uid,
			"source": "health_check"})
		if err := stmt.SetOption(ctx, models.OptionQueryTag, string(tag)); err != nil {
			return nil, pluginFault("setting the query tag", err)
		}
	}
	if err := stmt.SetSqlQuery(ctx, healthQuery); err != nil {
		return nil, err
	}
	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Release()
	rows := 0
	for reader.Next() {
		batch := reader.RecordBatch()
		if batch.NumRows() == 0 {
			continue
		}
		rows += int(batch.NumRows())
		for index, target := range []*string{&info.DuckDB, &info.Catalog, &info.Schema} {
			if text, ok := batch.Column(index).(*array.String); ok && !text.IsNull(0) {
				*target = strings.Clone(text.Value(0))
			}
		}
	}
	if err := reader.Err(); err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, fmt.Errorf("the health query returned %d rows, not 1", rows)
	}
	if info.Version == "" {
		info.Version = "(DuckDB " + info.DuckDB + ")"
	}
	return info, nil
}

// readServerInfo reads the server's name and version from the driver
// (ADBC's GetInfo, which the server answers without running SQL).
func readServerInfo(ctx context.Context, cnxn adbc.ConnectionWithContext, info *healthInfo) error {
	reader, err := cnxn.GetInfo(ctx, []adbc.InfoCode{adbc.InfoVendorName, adbc.InfoVendorVersion})
	if err != nil {
		return err
	}
	defer reader.Release()
	for reader.Next() {
		batch := reader.RecordBatch()
		if batch.NumCols() < 2 {
			continue
		}
		codes, ok := batch.Column(0).(*array.Uint32)
		values, isUnion := batch.Column(1).(*array.DenseUnion)
		if !ok || !isUnion {
			continue
		}
		for row := 0; row < int(batch.NumRows()); row++ {
			text, ok := values.Field(values.ChildID(row)).(*array.String)
			if !ok {
				continue
			}
			value := strings.Clone(text.Value(int(values.ValueOffset(row))))
			switch adbc.InfoCode(codes.Value(row)) {
			case adbc.InfoVendorName:
				if value != "" {
					info.Server = value
				}
			case adbc.InfoVendorVersion:
				info.Version = value
			}
		}
	}
	return reader.Err()
}
