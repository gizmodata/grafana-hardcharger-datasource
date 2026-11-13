package models

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/gizmodata/hardcharger-adbc/go/v2/hardcharger"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func source(t *testing.T, jsonData map[string]any, secure map[string]string) backend.DataSourceInstanceSettings {
	t.Helper()
	encoded, err := json.Marshal(jsonData)
	if err != nil {
		t.Fatal(err)
	}
	return backend.DataSourceInstanceSettings{JSONData: encoded, DecryptedSecureJSONData: secure}
}

var passwordSecrets = map[string]string{"password": "s3cret-pass"}

func TestConnectionURI(t *testing.T) {
	cases := []struct {
		name     string
		jsonData map[string]any
		want     string
		tls      bool
	}{
		{"host and port, TLS by default", map[string]any{"host": "db.example.com", "port": 31337}, "hardcharger://db.example.com:31337", true},
		{"TLS off", map[string]any{"host": "127.0.0.1", "port": 31337, "useTLS": false}, "hardcharger://127.0.0.1:31337?tls=false", false},
		{"TLS on explicitly", map[string]any{"host": "db.example.com", "port": 8443, "useTLS": true}, "hardcharger://db.example.com:8443", true},
		{"no port leaves it to the driver", map[string]any{"host": "db.example.com"}, "hardcharger://db.example.com", true},
		{"a port given as a string", map[string]any{"host": "db.example.com", "port": "4000"}, "hardcharger://db.example.com:4000", true},
		{"an IPv6 address", map[string]any{"host": "::1", "port": 31337, "useTLS": false}, "hardcharger://[::1]:31337?tls=false", false},
		{"an IPv6 address without a port", map[string]any{"host": "::1"}, "hardcharger://[::1]", true},
		{"a bracketed IPv6 address", map[string]any{"host": "[::1]", "port": 31337}, "hardcharger://[::1]:31337", true},
		{"a URI wins over host and port", map[string]any{"uri": " hardcharger://other.example.com:1234?tls=false ", "host": "ignored", "port": 1, "useTLS": true},
			"hardcharger://other.example.com:1234?tls=false", false},
		{"a URI is TLS unless it says so", map[string]any{"uri": "hardcharger://other.example.com"}, "hardcharger://other.example.com", true},
		{"an http URI is not TLS", map[string]any{"uri": "http://other.example.com:31337"}, "http://other.example.com:31337", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.jsonData["username"] = "joe"
			settings, err := LoadSettings(source(t, c.jsonData, passwordSecrets))
			if err != nil {
				t.Fatal(err)
			}
			if got := settings.ConnectionURI(); got != c.want {
				t.Errorf("ConnectionURI() = %q, want %q", got, c.want)
			}
			if settings.TLS() != c.tls {
				t.Errorf("TLS() = %v, want %v", settings.TLS(), c.tls)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	settings, err := LoadSettings(source(t, map[string]any{"host": "h", "username": "joe"}, passwordSecrets))
	if err != nil {
		t.Fatal(err)
	}
	if settings.AuthType != AuthTypePassword || settings.RowLimit() != DefaultMaxRows || settings.Timeout() != DefaultQueryTimeout ||
		!settings.TagsQueries() || !settings.TLS() {
		t.Errorf("defaults: %+v", settings)
	}
	settings, err = LoadSettings(source(t, map[string]any{"host": "h", "username": "joe", "maxRows": "250", "queryTimeout": 0,
		"tagQueries": false}, passwordSecrets))
	if err != nil {
		t.Fatal(err)
	}
	if settings.RowLimit() != 250 || settings.Timeout() != 0 || settings.TagsQueries() {
		t.Errorf("explicit values: rows %d, timeout %s, tags %v", settings.RowLimit(), settings.Timeout(), settings.TagsQueries())
	}
	settings, err = LoadSettings(source(t, map[string]any{"host": "h", "username": "joe", "queryTimeout": "2.5"}, passwordSecrets))
	if err != nil || settings.Timeout() != 2500*time.Millisecond {
		t.Errorf("a timeout given as a string: %v, %v", settings, err)
	}
}

func TestValidation(t *testing.T) {
	cert := map[string]string{"tlsClientCert": "CERT", "tlsClientKey": "KEY"}
	cases := []struct {
		name     string
		jsonData map[string]any
		secure   map[string]string
		want     string
	}{
		{"nothing configured", map[string]any{}, nil, "no server is configured"},
		{"no password", map[string]any{"host": "h", "username": "joe"}, nil, "a username and a password are required"},
		{"no username", map[string]any{"host": "h"}, passwordSecrets, "a username and a password are required"},
		{"token sign-in without a token", map[string]any{"host": "h", "authType": "token"}, passwordSecrets, "an access token is required"},
		{"certificate sign-in without a certificate", map[string]any{"host": "h", "authType": "client_certificate"}, nil,
			"needs the certificate and its private key"},
		{"certificate sign-in without TLS", map[string]any{"host": "h", "useTLS": false, "authType": "client_certificate"}, cert,
			"needs TLS"},
		{"a certificate without its key", map[string]any{"host": "h", "username": "joe"},
			map[string]string{"password": "p", "tlsClientCert": "CERT"}, "set both or neither"},
		{"an unknown sign-in method", map[string]any{"host": "h", "authType": "kerberos"}, nil, `unknown sign-in method "kerberos"`},
		{"a host with a scheme", map[string]any{"host": "https://h", "username": "joe"}, passwordSecrets, "must be a host name or an IP address"},
		{"a host with a path", map[string]any{"host": "h/path", "username": "joe"}, passwordSecrets, "must be a host name or an IP address"},
		{"a port out of range", map[string]any{"host": "h", "port": 70000, "username": "joe"}, passwordSecrets, "the port must be between 1 and 65535"},
		{"a port that is not a number", map[string]any{"host": "h", "port": "abc", "username": "joe"}, passwordSecrets, "expected a whole number"},
		{"a URI without a scheme", map[string]any{"uri": "db.example.com:31337", "username": "joe"}, passwordSecrets, "the connection URI is not valid"},
		{"too many rows", map[string]any{"host": "h", "username": "joe", "maxRows": 99999999}, passwordSecrets, "max rows must be between"},
		{"negative rows", map[string]any{"host": "h", "username": "joe", "maxRows": -1}, passwordSecrets, "max rows must be between"},
		{"a negative timeout", map[string]any{"host": "h", "username": "joe", "queryTimeout": -5}, passwordSecrets, "the query timeout must be between"},
		{"a wrong type", map[string]any{"host": 5, "username": "joe"}, passwordSecrets, `field "host" has the wrong type`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadSettings(source(t, c.jsonData, c.secure))
			if err == nil {
				t.Fatal("the settings were accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

// A URI with credentials is refused, and the refusal repeats neither the
// URI nor the password in it.
func TestURICredentialsAreRefusedWithoutEcho(t *testing.T) {
	for _, uri := range []string{
		"hardcharger://joe:hunter2-in-uri@db.example.com:31337",
		"hardcharger://joe@db.example.com",
		"https://joe:hunter2-in-uri@db.example.com/chargewire",
	} {
		_, err := LoadSettings(source(t, map[string]any{"uri": uri, "username": "joe"}, passwordSecrets))
		if err == nil {
			t.Fatalf("%s was accepted", uri)
		}
		if !strings.Contains(err.Error(), "must not contain credentials") {
			t.Errorf("error %q does not say why", err)
		}
		for _, leaked := range []string{"hunter2-in-uri", "db.example.com", "joe"} {
			if strings.Contains(err.Error(), leaked) {
				t.Errorf("the error repeats %q: %v", leaked, err)
			}
		}
	}
	// An unparseable URI is refused without being repeated either.
	_, err := LoadSettings(source(t, map[string]any{"uri": "hardcharger://joe:pw-with-%zz@h", "username": "joe"}, passwordSecrets))
	if err == nil || strings.Contains(err.Error(), "pw-with") {
		t.Errorf("an unparseable URI: %v", err)
	}
}

func TestDriverOptions(t *testing.T) {
	allSecrets := map[string]string{"password": "s3cret-pass", "token": "tok-123", "tlsCACert": "CA-PEM",
		"tlsClientCert": "CERT-PEM", "tlsClientKey": "KEY-PEM"}
	base := map[string]any{"host": "db.example.com", "port": 31337, "username": "joe", "tlsSkipVerify": true,
		"tlsServerName": "db.internal"}
	with := func(extra map[string]any) map[string]any {
		merged := map[string]any{}
		for key, value := range base {
			merged[key] = value
		}
		for key, value := range extra {
			merged[key] = value
		}
		return merged
	}
	tlsOptions := map[string]string{
		"adbc.chargewire.tls.skip_verify": "true", "adbc.chargewire.tls.root_certs": "CA-PEM",
		"adbc.chargewire.tls.server_name": "db.internal", "adbc.chargewire.tls.client_cert": "CERT-PEM",
		"adbc.chargewire.tls.client_key": "KEY-PEM",
	}
	cases := []struct {
		name     string
		jsonData map[string]any
		want     map[string]string
		absent   []string
	}{
		{"password", with(nil), merge(tlsOptions, map[string]string{"uri": "hardcharger://db.example.com:31337",
			"username": "joe", "password": "s3cret-pass"}), []string{"adbc.chargewire.auth.access_token", "adbc.hardcharger.auth_type"}},
		{"token", with(map[string]any{"authType": "token"}), merge(tlsOptions, map[string]string{
			"adbc.chargewire.auth.access_token": "tok-123"}), []string{"username", "password", "adbc.hardcharger.auth_type"}},
		{"client certificate", with(map[string]any{"authType": "client_certificate"}), merge(tlsOptions, map[string]string{
			"adbc.hardcharger.auth_type": "client_certificate"}), []string{"username", "password", "adbc.chargewire.auth.access_token"}},
		{"without TLS no TLS option is sent", with(map[string]any{"useTLS": false}), map[string]string{
			"uri": "hardcharger://db.example.com:31337?tls=false", "username": "joe", "password": "s3cret-pass"},
			[]string{"adbc.chargewire.tls.skip_verify", "adbc.chargewire.tls.root_certs", "adbc.chargewire.tls.server_name",
				"adbc.chargewire.tls.client_cert", "adbc.chargewire.tls.client_key"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings, err := LoadSettings(source(t, c.jsonData, allSecrets))
			if err != nil {
				t.Fatal(err)
			}
			options := settings.DriverOptions()
			for key, want := range c.want {
				if options[key] != want {
					t.Errorf("option %s = %q, want %q", key, options[key], want)
				}
			}
			for _, key := range c.absent {
				if value, present := options[key]; present {
					t.Errorf("option %s is set (%q)", key, value)
				}
			}
			if options["adbc.chargewire.statement.result_partitions"] != "1" {
				t.Errorf("result partitions: %q", options["adbc.chargewire.statement.result_partitions"])
			}
		})
	}
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for key, value := range m {
			out[key] = value
		}
	}
	return out
}

// Every option the plugin sets is one the driver has: the driver refuses an
// unknown key when the database is created (no server is contacted).
func TestDriverAcceptsEveryOption(t *testing.T) {
	allSecrets := map[string]string{"password": "s3cret-pass", "token": "tok-123", "tlsCACert": "CA-PEM",
		"tlsClientCert": "CERT-PEM", "tlsClientKey": "KEY-PEM"}
	seen := map[string]bool{}
	for _, authType := range []string{AuthTypePassword, AuthTypeToken, AuthTypeClientCertificate} {
		settings, err := LoadSettings(source(t, map[string]any{"host": "db.example.com", "username": "joe", "authType": authType,
			"tlsSkipVerify": true, "tlsServerName": "db.internal"}, allSecrets))
		if err != nil {
			t.Fatal(err)
		}
		options := settings.DriverOptions()
		for key := range options {
			seen[key] = true
		}
		db, err := hardcharger.NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(context.Background(), options)
		if err != nil {
			t.Fatalf("%s: the driver refused the options: %v", authType, err)
		}
		_ = db.Close(context.Background())
	}
	for _, key := range []string{"uri", "username", "password", optionAccessToken, optionAuthType, optionTLSSkipVerify,
		optionTLSRootCerts, optionTLSClientCert, optionTLSClientKey, optionTLSServerName, optionResultPartitions} {
		if !seen[key] {
			t.Errorf("option %s was never passed to the driver by this test", key)
		}
	}
	// The negative twin: a key the driver doesn't have is refused, so the
	// loop above would catch a misspelt option.
	_, err := hardcharger.NewDriver(memory.DefaultAllocator).NewDatabaseWithContext(context.Background(),
		map[string]string{"uri": "hardcharger://db.example.com", "adbc.chargewire.tls.no_such_option": "x"})
	if err == nil || !strings.Contains(err.Error(), "adbc.chargewire.tls.no_such_option") {
		t.Errorf("an unknown option was not refused: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	settings, err := LoadSettings(source(t, map[string]any{"host": "h", "username": "joe"},
		map[string]string{"password": "p1", "token": "t1", "tlsClientCert": "c1", "tlsClientKey": "k1", "tlsCACert": "ca"}))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(settings.Secrets(), ",")
	if got != "p1,t1,k1" {
		t.Errorf("Secrets() = %s", got)
	}
}
