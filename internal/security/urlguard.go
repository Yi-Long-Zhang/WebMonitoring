package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrUnsafeTarget = errors.New("target is outside the authorized public network boundary")

type Resolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type URLGuard struct {
	Resolver     Resolver
	AllowPrivate bool
	AllowedPorts map[string]struct{}
}

func NewURLGuard(allowPrivate bool) *URLGuard {
	return &URLGuard{
		Resolver:     net.DefaultResolver,
		AllowPrivate: allowPrivate,
		AllowedPorts: map[string]struct{}{"80": {}, "443": {}},
	}
}

func (g *URLGuard) Validate(ctx context.Context, rawURL string) (*url.URL, []net.IP, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse target URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, nil, fmt.Errorf("%w: only http and https are allowed", ErrUnsafeTarget)
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return nil, nil, fmt.Errorf("%w: URL credentials and empty hosts are forbidden", ErrUnsafeTarget)
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if _, allowed := g.AllowedPorts[port]; !allowed {
		return nil, nil, fmt.Errorf("%w: port %s is not allowed", ErrUnsafeTarget, port)
	}
	ips, err := g.Resolver.LookupIP(ctx, "ip", parsed.Hostname())
	if err != nil {
		return nil, nil, fmt.Errorf("resolve target host: %w", err)
	}
	if len(ips) == 0 {
		return nil, nil, errors.New("target host resolved to no addresses")
	}
	for _, ip := range ips {
		if !g.AllowPrivate && isUnsafeIP(ip) {
			return nil, nil, fmt.Errorf("%w: resolved address %s is blocked", ErrUnsafeTarget, ip)
		}
	}
	return parsed, ips, nil
}

func (g *URLGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse dial address: %w", err)
	}
	scheme := "http"
	if port == "443" {
		scheme = "https"
	}
	_, ips, err := g.Validate(ctx, scheme+"://"+net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("connect to validated target: %w", lastErr)
}

func isUnsafeIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return true
	}
	if address.Is4() {
		blocked := []string{
			"0.0.0.0/8",
			"100.64.0.0/10",
			"192.0.0.0/24",
			"192.0.2.0/24",
			"198.18.0.0/15",
			"198.51.100.0/24",
			"203.0.113.0/24",
			"224.0.0.0/4",
			"240.0.0.0/4",
		}
		for _, rawPrefix := range blocked {
			prefix := netip.MustParsePrefix(rawPrefix)
			if prefix.Contains(address) {
				return true
			}
		}
	}
	return strings.EqualFold(address.String(), "169.254.169.254")
}
