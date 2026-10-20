package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// ProxyCleanCustomSource describes a public text list, not an inference endpoint.
// DefaultProtocol applies only to bare IP:port entries, never to the HTTPS URL
// used to download the list. Explicit node schemes always take precedence.
type ProxyCleanCustomSource struct {
	URL             string `json:"url"`
	DefaultProtocol string `json:"default_protocol"`
}

func (s *ProxyCleanCustomSource) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &s.URL); err != nil {
			return err
		}
		s.DefaultProtocol = "socks5"
		return nil
	}
	type plain ProxyCleanCustomSource
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	*s = ProxyCleanCustomSource(v)
	return nil
}

func (s *ProxyCleanCustomSource) defaults() {
	s.URL = strings.TrimSpace(s.URL)
	s.DefaultProtocol = strings.ToLower(strings.TrimSpace(s.DefaultProtocol))
	if s.DefaultProtocol == "" {
		s.DefaultProtocol = "socks5"
	}
}

func (s ProxyCleanCustomSource) validate() error {
	if len(s.URL) > 2048 {
		return fmt.Errorf("proxyclean custom source URL exceeds 2048 bytes")
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return fmt.Errorf("proxyclean custom sources require public HTTPS URLs without credentials, query or fragment on port 443")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("proxyclean custom source must be public")
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		if ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("proxyclean custom source must be public")
		}
	}
	switch s.DefaultProtocol {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxyclean custom source default_protocol must be http, https, socks5 or socks5h")
	}
	return nil
}

func validateProxyCleanCustomSources(sources []ProxyCleanCustomSource) error {
	if len(sources) > 10 {
		return fmt.Errorf("proxyclean.custom_sources accepts at most 10 entries")
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if err := source.validate(); err != nil {
			return err
		}
		if seen[source.URL] {
			return fmt.Errorf("proxyclean.custom_sources contains duplicate URLs")
		}
		seen[source.URL] = true
	}
	return nil
}
