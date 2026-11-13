package plugin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// The live tests (TestLive*) run against real hardcharger-server processes
// that this file starts on free ports of 127.0.0.1. The server is a
// requirement: when its binary can't be found or doesn't start, the tests
// fail. Nothing skips.
//
// The binary is $HARDCHARGER_SERVER_BINARY or, without it, the newest one the
// `hardcharger` Python package downloaded (`pip install hardcharger &&
// python -m hardcharger download`), under ~/.cache/hardcharger.

const (
	serverBinaryEnv = "HARDCHARGER_SERVER_BINARY"
	testUsername    = "grafana_test"
	// adminIdentity is the identity of the client certificate that signs in
	// as the administrator on the TLS server.
	adminIdentity = "grafana@plugin-test.example"
)

// timeoutScale stretches the tests' waits on a slow machine.
var timeoutScale = func() float64 {
	if scale, err := strconv.ParseFloat(os.Getenv("GRAFANA_HARDCHARGER_TEST_TIMEOUT_SCALE"), 64); err == nil && scale > 0 {
		return scale
	}
	return 1
}()

func scaled(d time.Duration) time.Duration { return time.Duration(float64(d) * timeoutScale) }

func serverBinary() (string, error) {
	if path := os.Getenv(serverBinaryEnv); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s=%s: %w", serverBinaryEnv, path, err)
		}
		return path, nil
	}
	root := os.Getenv("HARDCHARGER_CACHE_DIR")
	if root == "" {
		cache := os.Getenv("XDG_CACHE_HOME")
		if cache == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			cache = filepath.Join(home, ".cache")
		}
		root = filepath.Join(cache, "hardcharger")
	}
	name := "hardcharger-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	found, _ := filepath.Glob(filepath.Join(root, "v*", "*", "bin", name))
	if len(found) == 0 {
		return "", fmt.Errorf("no hardcharger-server binary: set %s, or run `pip install hardcharger && "+
			"python -m hardcharger download` (looked in %s)", serverBinaryEnv, root)
	}
	sort.Slice(found, func(a, b int) bool { return versionLess(found[a], found[b]) })
	return found[len(found)-1], nil
}

// versionLess orders cache paths by the version directory in them.
func versionLess(a, b string) bool {
	parse := func(path string) []int {
		var numbers []int
		for _, part := range strings.Split(path, string(filepath.Separator)) {
			if strings.HasPrefix(part, "v") && strings.Count(part, ".") == 2 {
				for _, piece := range strings.Split(part[1:], ".") {
					n, _ := strconv.Atoi(piece)
					numbers = append(numbers, n)
				}
			}
		}
		return numbers
	}
	va, vb := parse(a), parse(b)
	for i := 0; i < len(va) && i < len(vb); i++ {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return len(va) < len(vb)
}

type testServer struct {
	Port     int
	Password string
	TLS      bool
	CA       *testCA
	cmd      *exec.Cmd
	stdin    interface{ Close() error }
	exited   chan struct{}
	logPath  string
}

func (s *testServer) Host() string { return "127.0.0.1" }

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

var (
	serversMu sync.Mutex
	servers   []*testServer
	serverDir string
	tempDirs  []string
)

// startServer starts a server with extra flags and waits until it answers.
func startServer(ca *testCA, extraArgs ...string) (*testServer, error) {
	binary, err := serverBinary()
	if err != nil {
		return nil, err
	}
	serversMu.Lock()
	if serverDir == "" {
		if serverDir, err = os.MkdirTemp("", "grafana-hardcharger-test-"); err != nil {
			serversMu.Unlock()
			return nil, err
		}
	}
	index := len(servers)
	serversMu.Unlock()
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 18)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	server := &testServer{Port: port, Password: hex.EncodeToString(secret), CA: ca, TLS: ca != nil,
		logPath: filepath.Join(serverDir, fmt.Sprintf("server-%d.log", index))}
	args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--health-port", "0",
		"--database", ":memory:", "--stop-on-stdin-close", "--no-banner"}
	if ca != nil {
		args = append(args, "--tls-cert", ca.serverCertFile, "--tls-key", ca.serverKeyFile)
	}
	args = append(args, extraArgs...)
	logFile, err := os.Create(server.logPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = logFile.Close() }()
	server.cmd = exec.Command(binary, args...)
	// Whatever the server writes next to its (in-memory) database, such as the instrumentation
	// tables' file, lands in the tests' temporary directory.
	server.cmd.Dir = serverDir
	server.cmd.Env = append(os.Environ(), "HARDCHARGER_ADMIN_USERNAME="+testUsername, "HARDCHARGER_ADMIN_PASSWORD="+server.Password)
	server.cmd.Stdout, server.cmd.Stderr = logFile, logFile
	// The server stops when this pipe closes, so it never outlives the tests.
	stdin, err := server.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	server.stdin = stdin
	if err := server.cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", binary, err)
	}
	serversMu.Lock()
	servers = append(servers, server)
	serversMu.Unlock()
	exited := make(chan error, 1)
	server.exited = make(chan struct{})
	go func() {
		exited <- server.cmd.Wait()
		close(server.exited)
	}()

	scheme := "http"
	if server.TLS {
		scheme = "https"
	}
	client := &http.Client{Timeout: 2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // a readiness probe of a test server
	url := fmt.Sprintf("%s://127.0.0.1:%d/chargewire", scheme, port)
	deadline := time.Now().Add(scaled(60 * time.Second))
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("hardcharger-server exited while starting (%v). Its output:\n%s", err, server.logTail())
		default:
		}
		if resp, err := client.Get(url); err == nil {
			_ = resp.Body.Close()
			return server, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("hardcharger-server did not answer at %s. Its output:\n%s", url, server.logTail())
}

func (s *testServer) logTail() string {
	content, _ := os.ReadFile(s.logPath)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) > 40 {
		lines = append(append(lines[:10:10], "..."), lines[len(lines)-30:]...)
	}
	return strings.Join(lines, "\n")
}

func stopServers() {
	serversMu.Lock()
	defer serversMu.Unlock()
	for _, server := range servers {
		_ = server.stdin.Close()
		select {
		case <-server.exited:
		case <-time.After(10 * time.Second):
			_ = server.cmd.Process.Kill()
		}
	}
	servers = nil
	for _, dir := range append(tempDirs, serverDir) {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	stopServers()
	os.Exit(code)
}

// The servers the live tests share, each started once, on first use.
var (
	plainOnce, tlsOnce, losslessOnce sync.Once
	plainSrv, tlsSrv, losslessSrv    *testServer
	plainErr, tlsErr, losslessErr    error
)

// plainServer is a server without TLS that records its statements
// (instrumentation), so the tests can see query tags and cancelled
// statements from the server's side.
func plainServer(t *testing.T) *testServer {
	t.Helper()
	plainOnce.Do(func() { plainSrv, plainErr = startServer(nil, "--enable-instrumentation") })
	if plainErr != nil {
		t.Fatalf("the live tests need a Hardcharger server: %v", plainErr)
	}
	return plainSrv
}

// tlsServer serves TLS with a certificate of a private CA, admits client
// certificates of that CA and lets adminIdentity's sign in.
func tlsServer(t *testing.T) *testServer {
	t.Helper()
	tlsOnce.Do(func() {
		var ca *testCA
		if ca, tlsErr = newTestCA(); tlsErr != nil {
			return
		}
		tlsSrv, tlsErr = startServer(ca, "--mtls-ca-cert", ca.caFile, "--no-mtls-require-client-cert", "--mtls-certificate-sign-in",
			"--mtls-admin-identities", adminIdentity)
	})
	if tlsErr != nil {
		t.Fatalf("the live tests need a Hardcharger server with TLS: %v", tlsErr)
	}
	return tlsSrv
}

// losslessServer sends DuckDB's own Arrow extension types (HUGEINT, UUID,
// BIT, TIMETZ, BOOLEAN as bool8): arrow_lossless_conversion.
func losslessServer(t *testing.T) *testServer {
	t.Helper()
	losslessOnce.Do(func() {
		losslessSrv, losslessErr = startServer(nil, "--init-sql-commands", "SET GLOBAL arrow_lossless_conversion = true")
	})
	if losslessErr != nil {
		t.Fatalf("the live tests need a Hardcharger server: %v", losslessErr)
	}
	return losslessSrv
}

// instanceSettings are a data source's settings for server, with overrides
// of jsonData and secureJsonData.
func instanceSettings(t *testing.T, server *testServer, jsonData map[string]any, secure map[string]string) backend.DataSourceInstanceSettings {
	t.Helper()
	merged := map[string]any{"host": server.Host(), "port": server.Port, "useTLS": server.TLS, "username": testUsername}
	for key, value := range jsonData {
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	secrets := map[string]string{"password": server.Password}
	if server.CA != nil {
		secrets["tlsCACert"] = server.CA.caPEM
	}
	for key, value := range secure {
		if value == "" {
			delete(secrets, key)
			continue
		}
		secrets[key] = value
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	return backend.DataSourceInstanceSettings{UID: "live-test", Name: "Hardcharger (test)", JSONData: encoded,
		DecryptedSecureJSONData: secrets, Updated: time.Now()}
}

// newLiveDatasource is a data source instance for server, disposed of when
// the test ends.
func newLiveDatasource(t *testing.T, server *testServer, jsonData map[string]any, secure map[string]string) *Datasource {
	t.Helper()
	instance, err := NewDatasource(context.Background(), instanceSettings(t, server, jsonData, secure))
	if err != nil {
		t.Fatalf("NewDatasource: %v", err)
	}
	datasource := instance.(*Datasource)
	t.Cleanup(datasource.Dispose)
	return datasource
}

// testCA is a throwaway certificate authority with a server certificate for
// 127.0.0.1.
type testCA struct {
	cert                                  *x509.Certificate
	key                                   *ecdsa.PrivateKey
	caPEM                                 string
	caFile, serverCertFile, serverKeyFile string
	serial                                int64
	mu                                    sync.Mutex
}

func newTestCA() (*testCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "grafana-hardcharger test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	ca := &testCA{cert: cert, key: key, serial: 1,
		caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
	dir, err := os.MkdirTemp("", "grafana-hardcharger-test-ca-")
	if err != nil {
		return nil, err
	}
	serversMu.Lock()
	tempDirs = append(tempDirs, dir)
	serversMu.Unlock()
	serverCert, serverKey, err := ca.issue("", true)
	if err != nil {
		return nil, err
	}
	for name, content := range map[string]string{"ca.pem": ca.caPEM, "server.pem": serverCert, "server-key.pem": serverKey} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	ca.caFile, ca.serverCertFile, ca.serverKeyFile = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "server.pem"),
		filepath.Join(dir, "server-key.pem")
	return ca, nil
}

// issue returns a certificate and its key (PEM): the server's, for
// 127.0.0.1, or a client's whose identity is the e-mail SAN email.
func (ca *testCA) issue(email string, server bool) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	ca.mu.Lock()
	ca.serial++
	serial := ca.serial
	ca.mu.Unlock()
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		template.Subject.CommonName = "hardcharger.test"
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.DNSNames, template.IPAddresses = []string{"hardcharger.test"}, []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.EmailAddresses = []string{email}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}
