package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mfow/llm-temporal-worker/config"
	"github.com/mfow/llm-temporal-worker/llm/provider"
)

const (
	defaultProviderRequestTimeout = 30 * time.Second
	maxProviderConnectTimeout     = 10 * time.Second
)

// ErrProviderEgressDenied identifies a provider request blocked before a
// connection can reach an unconfigured or unsafe destination. Its concrete
// errors intentionally contain classification only, never a URL, address, or
// request value.
var ErrProviderEgressDenied = provider.ErrProviderEgressDenied

// ProviderEgressResolver is the narrow DNS seam used by provider transports.
// Production uses net.DefaultResolver; tests can inject deterministic answers
// without allowing a hostname-only policy bypass.
type ProviderEgressResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// ProviderEgressDialContext is the narrow dialing seam used after a hostname
// has been resolved and every returned address has passed the egress policy.
// It receives an IP address rather than the original hostname.
type ProviderEgressDialContext func(context.Context, string, string) (net.Conn, error)

type providerEgressPolicy struct {
	allowedHosts   map[string]map[string]struct{}
	resolver       ProviderEgressResolver
	dial           ProviderEgressDialContext
	connectTimeout time.Duration
}

type providerEgressRoundTripper struct {
	policy *providerEgressPolicy
	next   http.RoundTripper
}

type providerEgressError struct {
	classification string
}

func (err providerEgressError) Error() string {
	return "provider egress blocked: " + err.classification
}

func (err providerEgressError) Unwrap() error { return ErrProviderEgressDenied }

func deniedProviderEgress(classification string) error {
	return providerEgressError{classification: classification}
}

func newProviderEgressHTTPClient(base *http.Client, endpoint config.EndpointConfig, resolver ProviderEgressResolver, dial ProviderEgressDialContext) (*http.Client, error) {
	timeout := boundedProviderRequestTimeout(time.Duration(endpoint.Timeout))
	policy, err := newProviderEgressPolicy(endpoint, resolver, dial, boundedProviderConnectTimeout(timeout))
	if err != nil {
		return nil, err
	}
	transport, err := cloneProviderTransport(base)
	if err != nil {
		return nil, err
	}
	transport.Proxy = nil
	transport.DialContext = policy.DialContext
	transport.DialTLSContext = nil
	transport.DialTLS = nil
	transport.TLSHandshakeTimeout = boundedProviderConnectTimeout(timeout)
	transport.ResponseHeaderTimeout = timeout
	transport.ExpectContinueTimeout = minDuration(time.Second, boundedProviderConnectTimeout(timeout))

	return &http.Client{
		Transport: &providerEgressRoundTripper{policy: policy, next: transport},
		// This bounds the complete response read. ResponseHeaderTimeout and the
		// bounded dial/TLS timeouts cover the earlier phases separately.
		Timeout: timeout,
		// Provider SDK clients must never follow a redirect implicitly. There
		// are no v1 redirecting provider endpoints; a future explicit redirect
		// mechanism must re-enter this policy for every hop.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func newProviderEgressPolicy(endpoint config.EndpointConfig, resolver ProviderEgressResolver, dial ProviderEgressDialContext, connectTimeout time.Duration) (*providerEgressPolicy, error) {
	allowedHosts, err := configuredProviderHosts(endpoint)
	if err != nil {
		return nil, err
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if connectTimeout <= 0 {
		connectTimeout = maxProviderConnectTimeout
	}
	if dial == nil {
		dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	return &providerEgressPolicy{
		allowedHosts:   allowedHosts,
		resolver:       resolver,
		dial:           dial,
		connectTimeout: connectTimeout,
	}, nil
}

func configuredProviderHosts(endpoint config.EndpointConfig) (map[string]map[string]struct{}, error) {
	if len(endpoint.OutboundHosts) == 0 {
		return nil, deniedProviderEgress("invalid_policy")
	}
	baseHost, basePort := "", ""
	if endpoint.BaseURL != "" {
		baseURL, parseErr := url.Parse(endpoint.BaseURL)
		if parseErr != nil || !strings.EqualFold(baseURL.Scheme, "https") || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
			return nil, deniedProviderEgress("invalid_policy")
		}
		var hostErr error
		baseHost, hostErr = config.NormalizeOutboundHost(baseURL.Hostname())
		if hostErr != nil {
			return nil, deniedProviderEgress("invalid_policy")
		}
		var portErr error
		basePort, portErr = egressURLPort(baseURL)
		if portErr != nil {
			return nil, deniedProviderEgress("invalid_policy")
		}
	}
	allowedHosts := make(map[string]map[string]struct{}, len(endpoint.OutboundHosts))
	for _, rawHost := range endpoint.OutboundHosts {
		host, err := config.NormalizeOutboundHost(rawHost)
		if err != nil {
			return nil, deniedProviderEgress("invalid_policy")
		}
		if _, duplicate := allowedHosts[host]; duplicate {
			return nil, deniedProviderEgress("invalid_policy")
		}
		ports := make(map[string]struct{}, 1)
		if host != baseHost {
			ports["443"] = struct{}{}
		} else {
			ports[basePort] = struct{}{}
		}
		allowedHosts[host] = ports
	}
	if endpoint.BaseURL == "" {
		return allowedHosts, nil
	}
	_, allowed := allowedHosts[baseHost]
	if !allowed {
		return nil, deniedProviderEgress("invalid_policy")
	}
	return allowedHosts, nil
}

func cloneProviderTransport(base *http.Client) (*http.Transport, error) {
	var transport *http.Transport
	if base != nil && base.Transport != nil {
		configured, ok := base.Transport.(*http.Transport)
		if !ok {
			return nil, deniedProviderEgress("invalid_transport")
		}
		transport = configured.Clone()
	} else {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, deniedProviderEgress("invalid_transport")
		}
		transport = defaultTransport.Clone()
	}
	if tlsConfig := transport.TLSClientConfig; tlsConfig != nil && (tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != "") {
		return nil, deniedProviderEgress("invalid_transport")
	}
	return transport, nil
}

func (guard *providerEgressRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if guard == nil || guard.policy == nil || guard.next == nil {
		return nil, deniedProviderEgress("invalid_transport")
	}
	if err := guard.policy.authorizeRequest(request); err != nil {
		return nil, err
	}
	return guard.next.RoundTrip(request)
}

func (policy *providerEgressPolicy) authorizeRequest(request *http.Request) error {
	if request == nil || request.URL == nil || !strings.EqualFold(request.URL.Scheme, "https") || request.URL.User != nil || request.URL.Fragment != "" {
		return deniedProviderEgress("invalid_request")
	}
	host, err := config.NormalizeOutboundHost(request.URL.Hostname())
	if err != nil {
		return deniedProviderEgress("invalid_request")
	}
	port, err := egressURLPort(request.URL)
	if err != nil {
		return deniedProviderEgress("invalid_request")
	}
	if !policy.allows(host, port) {
		return deniedProviderEgress("host_not_allowed")
	}
	if request.Host != "" {
		override := &url.URL{Host: request.Host}
		overrideHost, err := config.NormalizeOutboundHost(override.Hostname())
		if err != nil {
			return deniedProviderEgress("invalid_request")
		}
		overridePort, err := egressURLPort(override)
		if err != nil || overrideHost != host || overridePort != port {
			return deniedProviderEgress("invalid_request")
		}
	}
	return nil
}

func (policy *providerEgressPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if policy == nil || policy.resolver == nil || policy.dial == nil {
		return nil, deniedProviderEgress("invalid_policy")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, deniedProviderEgress("invalid_request")
	}
	host, err = config.NormalizeOutboundHost(host)
	if err != nil || !validEgressPort(port) || !policy.allows(host, port) {
		return nil, deniedProviderEgress("host_not_allowed")
	}
	// Bound DNS resolution and every subsequent TCP attempt by the same
	// connection deadline. net.Resolver honors this context, so a slow DNS
	// answer cannot outlive the provider connect budget.
	connectionContext, cancel := context.WithTimeout(ctx, policy.connectTimeout)
	defer cancel()
	addresses, err := policy.resolver.LookupIPAddr(connectionContext, host)
	if err != nil || len(addresses) == 0 {
		if connectionContext.Err() != nil {
			return nil, deniedProviderEgress("connection_timeout")
		}
		return nil, deniedProviderEgress("dns_resolution_failed")
	}
	resolved := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, candidate := range addresses {
		parsed, ok := netip.AddrFromSlice(candidate.IP)
		parsed = parsed.Unmap()
		if !ok || blockedProviderAddress(parsed) {
			return nil, deniedProviderEgress("unsafe_address")
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		resolved = append(resolved, parsed)
	}
	for _, target := range resolved {
		connection, dialErr := policy.dial(connectionContext, network, net.JoinHostPort(target.String(), port))
		if dialErr != nil {
			if connectionContext.Err() != nil {
				return nil, deniedProviderEgress("connection_timeout")
			}
			continue
		}
		if !policy.connectedToResolvedPublicAddress(connection, target) {
			_ = connection.Close()
			return nil, deniedProviderEgress("connected_address_denied")
		}
		return connection, nil
	}
	if connectionContext.Err() != nil {
		return nil, deniedProviderEgress("connection_timeout")
	}
	return nil, deniedProviderEgress("connection_failed")
}

func (policy *providerEgressPolicy) allows(host, port string) bool {
	ports, found := policy.allowedHosts[host]
	if !found {
		return false
	}
	_, allowed := ports[port]
	return allowed
}

func (policy *providerEgressPolicy) connectedToResolvedPublicAddress(connection net.Conn, target netip.Addr) bool {
	if connection == nil || connection.RemoteAddr() == nil {
		return false
	}
	remote, ok := providerRemoteAddress(connection.RemoteAddr())
	return ok && remote == target && !blockedProviderAddress(remote)
}

func providerRemoteAddress(address net.Addr) (netip.Addr, bool) {
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		parsed, valid := netip.AddrFromSlice(tcpAddress.IP)
		return parsed.Unmap(), valid
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return netip.Addr{}, false
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return parsed.Unmap(), true
}

func egressURLPort(value *url.URL) (string, error) {
	if value == nil {
		return "", errors.New("URL is nil")
	}
	port := value.Port()
	if port == "" {
		return "443", nil
	}
	if !validEgressPort(port) {
		return "", errors.New("invalid port")
	}
	return port, nil
}

func validEgressPort(value string) bool {
	port, err := strconv.ParseUint(value, 10, 16)
	return err == nil && port > 0
}

func boundedProviderRequestTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultProviderRequestTimeout
	}
	return timeout
}

func boundedProviderConnectTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > maxProviderConnectTimeout {
		return maxProviderConnectTimeout
	}
	return timeout
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

var providerMetadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("100.100.100.200"): {}, // Alibaba metadata service.
	netip.MustParseAddr("168.63.129.16"):   {}, // Azure platform metadata service.
	netip.MustParseAddr("169.254.169.254"): {}, // AWS, GCP, and EC2-compatible metadata.
	netip.MustParseAddr("fd00:ec2::254"):   {}, // AWS IMDS IPv6 endpoint.
}

var providerBlockedIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // RFC 6598 carrier-grade NAT.
	netip.MustParsePrefix("198.18.0.0/15"), // Benchmarking network.
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func blockedProviderAddress(address netip.Addr) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsMulticast() {
		return true
	}
	if _, metadata := providerMetadataAddresses[address]; metadata {
		return true
	}
	if address.Is4() {
		for _, prefix := range providerBlockedIPv4Prefixes {
			if prefix.Contains(address) {
				return true
			}
		}
	}
	return false
}
