// Package models holds the data source's settings: what the config editor
// saves, how it is validated, and the ADBC driver options it becomes.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Option keys of the Hardcharger ADBC driver and of the Chargewire driver it
// is built on. They are spelled out here, next to the settings they carry,
// and each one is checked against the driver by TestDriverAcceptsEveryOption.
const (
	optionAuthType         = "adbc.hardcharger.auth_type"
	optionAccessToken      = "adbc.chargewire.auth.access_token"
	optionTLSSkipVerify    = "adbc.chargewire.tls.skip_verify"
	optionTLSRootCerts     = "adbc.chargewire.tls.root_certs"
	optionTLSClientCert    = "adbc.chargewire.tls.client_cert"
	optionTLSClientKey     = "adbc.chargewire.tls.client_key"
	optionTLSServerName    = "adbc.chargewire.tls.server_name"
	optionResultPartitions = "adbc.chargewire.statement.result_partitions"

	// OptionStatementTimeout is the seconds after which the server cancels a
	// statement (a statement option).
	OptionStatementTimeout = "adbc.chargewire.statement.timeout_seconds"
	// OptionQueryTag is a JSON object the server records with a statement (a
	// statement option).
	OptionQueryTag = "adbc.chargewire.statement.query_tag"
)

// How the data source signs in.
const (
	AuthTypePassword          = "password"
	AuthTypeToken             = "token"
	AuthTypeClientCertificate = "client_certificate"
)

// Defaults and bounds.
const (
	// DefaultMaxRows is the row cap of a query when the setting is unset.
	DefaultMaxRows = 100_000
	// MaxMaxRows is the largest row cap the setting accepts.
	MaxMaxRows = 10_000_000
	// DefaultQueryTimeout is a query's time limit when the setting is unset.
	DefaultQueryTimeout = 5 * time.Minute
	// MaxQueryTimeout is the longest time limit the setting accepts.
	MaxQueryTimeout = 24 * time.Hour
)

// Settings is the data source's configuration: jsonData, plus the secure
// fields Grafana decrypts for the backend.
type Settings struct {
	// URI is a whole hardcharger:// connection URI. When set, Host, Port and
	// UseTLS are ignored. It never carries credentials.
	URI string `json:"uri"`
	// Host and Port name the server when URI is empty. Port 0 leaves the
	// port to the driver: 31337, or 443 for a GizmoData Cloud host.
	Host string  `json:"host"`
	Port flexInt `json:"port"`
	// UseTLS is on unless it is set to false, as hardcharger:// URIs are
	// HTTPS unless they say ?tls=false.
	UseTLS *bool `json:"useTLS"`
	// TLSSkipVerify turns off verification of the server's certificate.
	TLSSkipVerify bool `json:"tlsSkipVerify"`
	// TLSServerName overrides the name the server's certificate is checked
	// against.
	TLSServerName string `json:"tlsServerName"`

	// AuthType is AuthTypePassword (the default), AuthTypeToken or
	// AuthTypeClientCertificate.
	AuthType string `json:"authType"`
	Username string `json:"username"`

	// Catalog and Schema are where every query's session starts; empty
	// leaves the server's default.
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`

	// MaxRows caps the rows read from one query (0: DefaultMaxRows).
	MaxRows flexInt `json:"maxRows"`
	// QueryTimeout is a query's time limit in seconds (unset:
	// DefaultQueryTimeout; 0: none).
	QueryTimeout *flexFloat `json:"queryTimeout"`
	// TagQueries records Grafana's context (dashboard, panel, user) with
	// each statement on the server. On unless set to false.
	TagQueries *bool `json:"tagQueries"`

	// Secure fields.
	Password      string `json:"-"`
	Token         string `json:"-"`
	TLSCACert     string `json:"-"`
	TLSClientCert string `json:"-"`
	TLSClientKey  string `json:"-"`
}

// flexInt is an integer setting that provisioning files may also give as a
// string ("31337").
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	text := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("expected a whole number, got %s", string(b))
	}
	*f = flexInt(n)
	return nil
}

// flexFloat is a numeric setting that may also be given as a string.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	text := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if text == "null" {
		return nil
	}
	if text == "" {
		return errors.New("expected a number, got an empty string")
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("expected a number, got %s", string(b))
	}
	*f = flexFloat(n)
	return nil
}

// LoadSettings reads and validates a data source instance's settings. Its
// errors are safe to show: they never repeat a secret or the URI.
func LoadSettings(source backend.DataSourceInstanceSettings) (*Settings, error) {
	settings := &Settings{}
	if len(source.JSONData) > 0 {
		if err := json.Unmarshal(source.JSONData, settings); err != nil {
			return nil, fmt.Errorf("the data source's settings could not be read: %w", jsonError(err))
		}
	}
	secure := source.DecryptedSecureJSONData
	settings.Password = secure["password"]
	settings.Token = secure["token"]
	settings.TLSCACert = secure["tlsCACert"]
	settings.TLSClientCert = secure["tlsClientCert"]
	settings.TLSClientKey = secure["tlsClientKey"]
	settings.URI = strings.TrimSpace(settings.URI)
	settings.Host = strings.TrimSpace(settings.Host)
	settings.Username = strings.TrimSpace(settings.Username)
	settings.Catalog = strings.TrimSpace(settings.Catalog)
	settings.Schema = strings.TrimSpace(settings.Schema)
	settings.TLSServerName = strings.TrimSpace(settings.TLSServerName)
	settings.AuthType = strings.ToLower(strings.TrimSpace(settings.AuthType))
	if settings.AuthType == "" {
		settings.AuthType = AuthTypePassword
	}
	if err := settings.validate(); err != nil {
		return nil, err
	}
	return settings, nil
}

// jsonError drops the offending value from a JSON type error, keeping the
// field it was for.
func jsonError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Errorf("field %q has the wrong type (%s)", typeErr.Field, typeErr.Value)
	}
	return err
}

func (s *Settings) validate() error {
	if s.URI == "" && s.Host == "" {
		return errors.New("no server is configured: set a host or a connection URI")
	}
	if s.URI != "" {
		parsed, err := url.Parse(s.URI)
		if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
			return errors.New("the connection URI is not valid: expected hardcharger://host[:port], " +
				"or hardcharger://host:port?tls=false for a server without TLS")
		}
		if parsed.User != nil {
			return errors.New("the connection URI must not contain credentials: use the username, password " +
				"or access token fields")
		}
	} else {
		if strings.ContainsAny(s.Host, "/?#@ ") || strings.Contains(s.Host, "://") {
			return errors.New("the host must be a host name or an IP address, without a scheme, a path or " +
				"credentials (to give a whole URI, use the connection URI field)")
		}
		if s.Port < 0 || s.Port > 65535 {
			return fmt.Errorf("the port must be between 1 and 65535, got %d", s.Port)
		}
	}
	switch s.AuthType {
	case AuthTypePassword:
		if s.Username == "" || s.Password == "" {
			return errors.New("a username and a password are required (or choose another sign-in method)")
		}
	case AuthTypeToken:
		if s.Token == "" {
			return errors.New("an access token is required (or choose another sign-in method)")
		}
	case AuthTypeClientCertificate:
		if s.TLSClientCert == "" || s.TLSClientKey == "" {
			return errors.New("signing in with a client certificate needs the certificate and its private key")
		}
		if !s.TLS() {
			return errors.New("signing in with a client certificate needs TLS")
		}
	default:
		return fmt.Errorf("unknown sign-in method %q: expected %s, %s or %s", s.AuthType,
			AuthTypePassword, AuthTypeToken, AuthTypeClientCertificate)
	}
	if (s.TLSClientCert == "") != (s.TLSClientKey == "") {
		return errors.New("a TLS client certificate and its private key go together: set both or neither")
	}
	if s.MaxRows < 0 || s.MaxRows > MaxMaxRows {
		return fmt.Errorf("max rows must be between 1 and %d (0 or empty: %d), got %d", MaxMaxRows,
			DefaultMaxRows, s.MaxRows)
	}
	if s.QueryTimeout != nil {
		if seconds := float64(*s.QueryTimeout); seconds < 0 || seconds > MaxQueryTimeout.Seconds() {
			return fmt.Errorf("the query timeout must be between 0 (none) and %.0f seconds, got %v",
				MaxQueryTimeout.Seconds(), seconds)
		}
	}
	return nil
}

// TLS reports whether the connection uses TLS. For a connection URI it is
// read from the URI as the driver reads it: on, unless the scheme is http or
// the URI says tls=false.
func (s *Settings) TLS() bool {
	if s.URI == "" {
		return s.UseTLS == nil || *s.UseTLS
	}
	parsed, err := url.Parse(s.URI)
	if err != nil {
		return true
	}
	if strings.EqualFold(parsed.Scheme, "http") {
		return false
	}
	switch strings.ToLower(parsed.Query().Get("tls")) {
	case "false", "0":
		return false
	}
	return true
}

// RowLimit is the most rows read from one query.
func (s *Settings) RowLimit() int64 {
	if s.MaxRows <= 0 {
		return DefaultMaxRows
	}
	return int64(s.MaxRows)
}

// Timeout is a query's time limit; 0 means none.
func (s *Settings) Timeout() time.Duration {
	if s.QueryTimeout == nil {
		return DefaultQueryTimeout
	}
	return time.Duration(float64(*s.QueryTimeout) * float64(time.Second))
}

// TagsQueries reports whether statements are tagged with Grafana's context.
func (s *Settings) TagsQueries() bool {
	return s.TagQueries == nil || *s.TagQueries
}

// ConnectionURI is the URI handed to the driver.
func (s *Settings) ConnectionURI() string {
	if s.URI != "" {
		return s.URI
	}
	host := s.Host
	if s.Port > 0 {
		host = net.JoinHostPort(strings.Trim(host, "[]"), strconv.FormatInt(int64(s.Port), 10))
	} else if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // a bare IPv6 address
	}
	uri := "hardcharger://" + host
	if !s.TLS() {
		uri += "?tls=false"
	}
	return uri
}

// DriverOptions are the options the ADBC database is created with. The map
// holds secrets: it is never logged.
func (s *Settings) DriverOptions() map[string]string {
	options := map[string]string{
		adbc.OptionKeyURI: s.ConnectionURI(),
		// One connection per result: a panel's rows are few, and they are read
		// in order up to the row cap.
		optionResultPartitions: "1",
	}
	switch s.AuthType {
	case AuthTypeToken:
		options[optionAccessToken] = s.Token
	case AuthTypeClientCertificate:
		options[optionAuthType] = AuthTypeClientCertificate
	default:
		options[adbc.OptionKeyUsername] = s.Username
		options[adbc.OptionKeyPassword] = s.Password
	}
	if s.TLS() {
		if s.TLSSkipVerify {
			options[optionTLSSkipVerify] = adbc.OptionValueEnabled
		}
		if s.TLSCACert != "" {
			options[optionTLSRootCerts] = s.TLSCACert
		}
		if s.TLSServerName != "" {
			options[optionTLSServerName] = s.TLSServerName
		}
		if s.TLSClientCert != "" {
			options[optionTLSClientCert] = s.TLSClientCert
			options[optionTLSClientKey] = s.TLSClientKey
		}
	}
	return options
}

// Secrets are the values that must never appear in an error or a log line.
func (s *Settings) Secrets() []string {
	var secrets []string
	for _, value := range []string{s.Password, s.Token, s.TLSClientKey} {
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}
