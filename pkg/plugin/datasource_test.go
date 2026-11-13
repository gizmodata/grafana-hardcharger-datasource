package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
)

// offlineSettings point at a port nothing listens on: enough for everything
// that happens before a query reaches the server.
func offlineSettings(t *testing.T, jsonData map[string]any, secure map[string]string) backend.DataSourceInstanceSettings {
	t.Helper()
	if jsonData == nil {
		jsonData = map[string]any{"host": "127.0.0.1", "port": 1, "useTLS": false, "username": "joe"}
	}
	if secure == nil {
		secure = map[string]string{"password": "offline-password"}
	}
	encoded, err := json.Marshal(jsonData)
	if err != nil {
		t.Fatal(err)
	}
	return backend.DataSourceInstanceSettings{ID: 7, UID: "offline", JSONData: encoded, DecryptedSecureJSONData: secure,
		Updated: time.Unix(1_700_000_000, 0)}
}

func offlineDatasource(t *testing.T, jsonData map[string]any, secure map[string]string) *Datasource {
	t.Helper()
	instance, err := NewDatasource(context.Background(), offlineSettings(t, jsonData, secure))
	if err != nil {
		t.Fatalf("NewDatasource: %v", err)
	}
	ds := instance.(*Datasource)
	t.Cleanup(ds.Dispose)
	return ds
}

func TestQueriesRefusedBeforeTheServer(t *testing.T) {
	ds := offlineDatasource(t, nil, nil)
	cases := []struct {
		name   string
		sql    string
		format string
		want   string
	}{
		{"an empty query", "  \n ", "table", "the query is empty"},
		{"an unknown format", "SELECT 1", "heatmap", `unknown format "heatmap"`},
		{"an unknown macro", "SELECT $__nope()", "table", "unknown macro $__nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := runQuery(t, ds, c.sql, queryOptions{Format: c.format})
			wantFailure(t, result, backend.StatusBadRequest, backend.ErrorSourceDownstream, c.want)
		})
	}
	// A query that is not JSON at all.
	response, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: []byte(`{"rawSql": 5}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	wantFailure(t, response.Responses["A"], backend.StatusBadRequest, backend.ErrorSourceDownstream, "the query could not be read")
}

// Settings that cannot work never fail the instance: the health check and
// every query say what is wrong, as the configuration's error.
func TestUnusableSettings(t *testing.T) {
	ds := offlineDatasource(t, map[string]any{"host": "127.0.0.1", "username": "joe"}, map[string]string{})
	health, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != backend.HealthStatusError || !strings.Contains(health.Message, "a username and a password are required") {
		t.Errorf("health: %v %q", health.Status, health.Message)
	}
	result := runQuery(t, ds, "SELECT 1", queryOptions{})
	wantFailure(t, result, backend.StatusBadRequest, backend.ErrorSourceDownstream,
		"the data source is not configured correctly", "a username and a password are required")
}

// A server that is not there is the server's failure (502, downstream), and
// neither the query's error nor the health check's repeats the password.
func TestUnreachableServer(t *testing.T) {
	ds := offlineDatasource(t, nil, nil)
	result := runQuery(t, ds, "SELECT 1", queryOptions{})
	wantFailure(t, result, backend.StatusBadGateway, backend.ErrorSourceDownstream, "connection refused")
	health, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != backend.HealthStatusError || !strings.Contains(health.Message, "The server could not be reached") {
		t.Errorf("health: %v %q", health.Status, health.Message)
	}
	for _, text := range []string{result.Error.Error(), health.Message} {
		if strings.Contains(text, "offline-password") {
			t.Errorf("the password is in %q", text)
		}
	}
}

func TestQueryTag(t *testing.T) {
	ds := offlineDatasource(t, nil, nil)
	request := &backend.QueryDataRequest{
		PluginContext: backend.PluginContext{OrgID: 3, User: &backend.User{Login: "alice"}},
		Headers: map[string]string{"http_X-Dashboard-Uid": "dash-1", "http_X-Panel-Id": "12", "http_X-Rule-Uid": "rule-9",
			"FromAlert": "true"},
	}
	var tag map[string]any
	if err := json.Unmarshal([]byte(ds.queryTag(request, backend.DataQuery{RefID: "B"})), &tag); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"app": "grafana", "plugin": PluginID, "datasource_uid": "offline", "ref_id": "B", "org_id": 3.0,
		"user": "alice", "dashboard_uid": "dash-1", "panel_id": "12", "rule_uid": "rule-9", "from_alert": true}
	if len(tag) != len(want) {
		t.Errorf("tag %v", tag)
	}
	for key, value := range want {
		if tag[key] != value {
			t.Errorf("tag[%s] = %v, want %v", key, tag[key], value)
		}
	}
	// Without a user or headers, only what is known.
	tag = nil
	if err := json.Unmarshal([]byte(ds.queryTag(&backend.QueryDataRequest{}, backend.DataQuery{RefID: "A"})), &tag); err != nil {
		t.Fatal(err)
	}
	if len(tag) != 4 {
		t.Errorf("a bare request's tag: %v", tag)
	}
	// A long value is clipped, so the tag stays under the protocol's 4,096 bytes.
	long := &backend.QueryDataRequest{PluginContext: backend.PluginContext{User: &backend.User{Login: strings.Repeat("é", 3000)}}}
	if encoded := ds.queryTag(long, backend.DataQuery{RefID: strings.Repeat("r", 5000)}); len(encoded) > 1024 || !json.Valid([]byte(encoded)) {
		t.Errorf("a long tag: %d bytes", len(encoded))
	}
	// Turned off.
	silent := offlineDatasource(t, map[string]any{"host": "127.0.0.1", "port": 1, "useTLS": false, "username": "joe",
		"tagQueries": false}, nil)
	if tag := silent.queryTag(request, backend.DataQuery{RefID: "A"}); tag != "" {
		t.Errorf("tagging is off, got %s", tag)
	}
}

func TestQueryInterval(t *testing.T) {
	hour := backend.TimeRange{From: time.Unix(0, 0), To: time.Unix(3600, 0)}
	if got := queryInterval(backend.DataQuery{Interval: 15 * time.Second, MaxDataPoints: 100, TimeRange: hour}); got != 15*time.Second {
		t.Errorf("Grafana's interval: %s", got)
	}
	if got := queryInterval(backend.DataQuery{MaxDataPoints: 100, TimeRange: hour}); got != 36*time.Second {
		t.Errorf("from the points wanted: %s", got)
	}
	if got := queryInterval(backend.DataQuery{}); got != time.Second {
		t.Errorf("with nothing to go by: %s", got)
	}
}

// One instance, and so one database handle, per settings version: Grafana's
// instance manager asks for a new one when the settings' Updated time
// changes, and disposes of the old one, whose handle is then closed.
func TestNewSettingsGetANewInstance(t *testing.T) {
	var created atomic.Int32
	provider := datasource.NewInstanceProvider(func(ctx context.Context, settings backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
		created.Add(1)
		return NewDatasource(ctx, settings)
	})
	manager := instancemgmt.New(provider)
	settings := offlineSettings(t, nil, nil)
	pluginContext := func() backend.PluginContext {
		copied := settings
		return backend.PluginContext{OrgID: 1, PluginID: PluginID, DataSourceInstanceSettings: &copied}
	}
	first, err := manager.Get(context.Background(), pluginContext())
	if err != nil {
		t.Fatal(err)
	}
	again, err := manager.Get(context.Background(), pluginContext())
	if err != nil {
		t.Fatal(err)
	}
	if first != again || created.Load() != 1 {
		t.Fatalf("unchanged settings gave another instance (%d created)", created.Load())
	}
	if first.(*Datasource).db == nil {
		t.Fatal("the instance has no database handle")
	}

	settings.Updated = settings.Updated.Add(time.Minute) // saved again
	second, err := manager.Get(context.Background(), pluginContext())
	if err != nil {
		t.Fatal(err)
	}
	if second == first || created.Load() != 2 {
		t.Fatalf("changed settings kept the old instance (%d created)", created.Load())
	}
	if second.(*Datasource).db == first.(*Datasource).db {
		t.Fatal("the new instance shares the old one's database handle")
	}
	// The manager disposes of the replaced instance after its grace period (5 s).
	old := first.(*Datasource)
	deadline := time.Now().Add(scaled(20 * time.Second))
	for {
		old.gate.RLock()
		closed := old.closed
		old.gate.RUnlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replaced instance was never disposed of")
		}
		time.Sleep(100 * time.Millisecond)
	}
	result := runQuery(t, old, "SELECT 1", queryOptions{})
	wantFailure(t, result, backend.StatusInternal, backend.ErrorSourcePlugin, "was replaced by newer settings")
	// The new instance is untouched.
	second.(*Datasource).gate.RLock()
	stillOpen := !second.(*Datasource).closed
	second.(*Datasource).gate.RUnlock()
	if !stillOpen {
		t.Error("the new instance was closed too")
	}
	second.(*Datasource).Dispose()
}
