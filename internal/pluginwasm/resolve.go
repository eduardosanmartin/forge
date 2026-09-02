package pluginwasm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
)

// HostResolver abstracts DNS resolution for net_fetch host validation.
// It is injected so tests can simulate DNS rebinding without real DNS.
type HostResolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// defaultResolver uses the system resolver via net.DefaultResolver.
type defaultResolver struct{}

func (d *defaultResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// resolveHost resolves host to IPs, handling IP literals without DNS.
// For an IP literal it returns the single IP; otherwise it delegates to resolver.
func resolveHost(ctx context.Context, resolver HostResolver, host string) ([]net.IP, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, fmt.Errorf("empty host")
	}
	// Strip brackets that url.Hostname may have already removed, but keep defensive.
	clean := strings.Trim(trimmed, "[]")
	if ip := net.ParseIP(clean); ip != nil {
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		resolver = &defaultResolver{}
	}
	ips, err := resolver.LookupIP(ctx, trimmed)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs for host %q", host)
	}
	return ips, nil
}

// buildAllowedIPSet derives the allowed IP set from the allowlist.
// - IP literal entries contribute directly.
// - CIDR entries contribute the network (stored as *net.IPNet).
// - Hostname entries are resolved via resolver; on resolve error they contribute nothing (debug logged).
//
// Per-call resolution (no caching): every net_fetch call re-resolves hostname entries
// fresh. This avoids TOCTOU where a cached entry could authorize a stale/rebound IP.
// A tiny TTL cache could reduce DNS load but adds risk of using stale data during rebinding;
// per-call is safer and matches the strict pin semantics.
//
// Returns a map of canonical IP string -> struct{} and a slice of CIDR networks.
func buildAllowedIPSet(ctx context.Context, resolver HostResolver, allowlist []string, logger *slog.Logger) (map[string]struct{}, []*net.IPNet) {
	allowedIPs := make(map[string]struct{})
	var cidrs []*net.IPNet
	if resolver == nil {
		resolver = &defaultResolver{}
	}
	for _, entry := range allowlist {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		// CIDR first: net.ParseCIDR handles "192.168.0.0/16" etc.
		if _, ipnet, err := net.ParseCIDR(e); err == nil {
			cidrs = append(cidrs, ipnet)
			continue
		}
		// IP literal (handle bracketed IPv6 like "[::1]")
		cleanE := strings.Trim(e, "[]")
		if ip := net.ParseIP(cleanE); ip != nil {
			allowedIPs[ip.String()] = struct{}{}
			continue
		}
		// Hostname entry: strip port if present (e.g. "example.com:8080")
		hostEntry := e
		// Try net.SplitHostPort for proper handling of "[::1]:8080" style (though hostname entries unlikely to contain port)
		if h, _, err := net.SplitHostPort(e); err == nil {
			hostEntry = h
		} else {
			// Defensive: strip brackets and, if there's a colon with numeric port suffix, treat as host:port.
			// We already handled IP literals, so remaining colon likely means host:port for hostname.
			// Detect trailing numeric port.
			if idx := strings.LastIndex(hostEntry, ":"); idx != -1 {
				// Check if suffix is all digits.
				suffix := hostEntry[idx+1:]
				isPort := suffix != "" && suffix != hostEntry
				if isPort {
					allDigits := true
					for _, c := range suffix {
						if c < '0' || c > '9' {
							allDigits = false
							break
						}
					}
					// Only strip if before colon does not look like IPv6 (we already handled IP literals, so it's safe)
					if allDigits {
						hostEntry = hostEntry[:idx]
						hostEntry = strings.Trim(hostEntry, "[]")
					}
				}
			} else {
				hostEntry = strings.Trim(hostEntry, "[]")
			}
		}
		hostEntry = strings.TrimSpace(hostEntry)
		if hostEntry == "" {
			continue
		}
		ips, err := resolveHost(ctx, resolver, hostEntry)
		if err != nil {
			if logger != nil {
				logger.Debug("allowlist hostname resolve failed", slog.String("entry", e), slog.String("host", hostEntry), slog.String("error", err.Error()))
			}
			continue
		}
		for _, ip := range ips {
			allowedIPs[ip.String()] = struct{}{}
		}
	}
	return allowedIPs, cidrs
}

// isIPAllowed reports whether ip is allowed by the derived sets.
// It checks exact IP map and containment in any CIDR.
func isIPAllowed(ip net.IP, allowedIPs map[string]struct{}, cidrs []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if _, ok := allowedIPs[ip.String()]; ok {
		return true
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// allIPsStrictAllowed implements the chosen strict semantics:
// at least one resolved IP must be present AND every resolved IP must be allowed.
// If any resolved IP is disallowed, the whole target is denied. This is safer against
// mixed rebinding where one A record is legit and another is internal (e.g. 127.0.0.1).
// An entry with only IPv4 cannot authorize an IPv6 resolution because the IPv6 address
// will not be in the allowed set (no CIDR/IP match), thus strict denies.
func allIPsStrictAllowed(targetIPs []net.IP, allowedIPs map[string]struct{}, cidrs []*net.IPNet) bool {
	if len(targetIPs) == 0 {
		return false
	}
	for _, ip := range targetIPs {
		if !isIPAllowed(ip, allowedIPs, cidrs) {
			return false
		}
	}
	return true
}

// isHostAllowed checks whether urlStr's host is in the allowlist.
// Updated to correctly handle IPv6 literals, ports, IP literals, and CIDR entries while
// preserving backward-compatible semantics:
//   - empty allowlist => deny all
//   - exact host or suffix match (example.com allows api.example.com)
//   - IP literal equality, CIDR containment for IP hosts
//   - port stripping, bracket handling, case-insensitive hostname compare
func isHostAllowed(urlStr string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		return false
	}
	host := u.Hostname() // correctly strips port and brackets, handles IPv6
	if host == "" {
		return false
	}
	hostLower := strings.ToLower(host)
	hostIP := net.ParseIP(host)

	for _, entry := range allowlist {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		// CIDR entry: check containment if host is IP literal
		if _, ipnet, err := net.ParseCIDR(e); err == nil {
			if hostIP != nil && ipnet.Contains(hostIP) {
				return true
			}
			continue
		}
		// IP literal entry (handle bracketed IPv6)
		cleanE := strings.Trim(e, "[]")
		if ip := net.ParseIP(cleanE); ip != nil {
			if hostIP != nil && ip.Equal(hostIP) {
				return true
			}
			// Also compare string forms for hostLower IP (covers canonicalization)
			if hostIP != nil && ip.String() == hostIP.String() {
				return true
			}
			continue
		}
		// Hostname entry: strip port if present
		entryHost := e
		if h, _, err := net.SplitHostPort(e); err == nil {
			entryHost = h
		} else {
			// Handle "hostname:port" without brackets where hostname not IP
			// Detect numeric port suffix
			if idx := strings.LastIndex(entryHost, ":"); idx != -1 {
				suffix := entryHost[idx+1:]
				isPort := suffix != ""
				if isPort {
					for _, c := range suffix {
						if c < '0' || c > '9' {
							isPort = false
							break
						}
					}
					if isPort {
						// Ensure not misclassifying; IP literals already handled, so safe to strip
						entryHost = entryHost[:idx]
					}
				}
			}
			entryHost = strings.Trim(entryHost, "[]")
		}
		entryHost = strings.TrimSpace(entryHost)
		if entryHost == "" {
			continue
		}
		eLower := strings.ToLower(entryHost)
		if hostLower == eLower || strings.HasSuffix(hostLower, "."+eLower) {
			return true
		}
	}
	return false
}
