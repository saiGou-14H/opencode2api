package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
)

const chinaSourceMaxBytes = 4 << 20

// parseChinaSource selects publisher-labelled CN candidates, not verified CN
// exits. In particular monosans exit_ip is metadata, never admission evidence.
// The entire bounded body is validated even after the candidate limit is met.
func parseChinaSource(id string, body io.Reader, limit int) ([]string, error) {
	if id != "xyzs-cn" && id != "monosans-cn" {
		return nil, errors.New("source_unknown")
	}
	if body == nil || limit <= 0 {
		return nil, errors.New("source_invalid")
	}
	data, err := io.ReadAll(io.LimitReader(body, chinaSourceMaxBytes+1))
	if err != nil {
		return nil, errors.New("source_invalid")
	}
	if len(data) > chinaSourceMaxBytes {
		return nil, errors.New("source_too_large")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '[' {
		return nil, errors.New("source_invalid")
	}
	var rows []json.RawMessage
	if json.Unmarshal(data, &rows) != nil {
		return nil, errors.New("source_invalid")
	}
	var out []string
	seen := make(map[string]bool)
	for _, raw := range rows {
		var row struct {
			Host        string          `json:"host"`
			Port        int             `json:"port"`
			Protocol    string          `json:"protocol"`
			Country     string          `json:"country"`
			CheckType   string          `json:"checkType"`
			Username    json.RawMessage `json:"username"`
			Password    json.RawMessage `json:"password"`
			Geolocation struct {
				Country struct {
					ISOCode string `json:"iso_code"`
				} `json:"country"`
			} `json:"geolocation"`
		}
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		// Null/absent credentials are the actual public monosans schema. Reject
		// all other credential values, including malformed types and empty strings.
		if chinaSourceHasCredential(row.Username) || chinaSourceHasCredential(row.Password) {
			continue
		}
		country := row.Country
		if id == "monosans-cn" {
			country = row.Geolocation.Country.ISOCode
		}
		if country != "CN" {
			continue
		}
		protocol := row.Protocol
		if id == "xyzs-cn" && protocol == "https" {
			if row.CheckType != "https_connect_probe" {
				continue
			}
			protocol = "http"
		}
		switch protocol {
		case "http", "https", "socks5", "socks5h":
		default:
			continue
		}
		// Parse host before constructing a URL: never allow userinfo, URL syntax,
		// comments or a hostname to be smuggled through the structured schema.
		ip, err := netip.ParseAddr(row.Host)
		if err != nil || !publicProxyIP(ip) || row.Port < 1 || row.Port > 65535 {
			continue
		}
		normalized := normalizeProxyCandidate(protocol+"://"+net.JoinHostPort(ip.String(), strconv.Itoa(row.Port)), "", true)
		if normalized == "" || seen[normalized] {
			continue
		}
		if len(out) < limit {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("source_empty")
	}
	return out, nil
}

func chinaSourceHasCredential(value json.RawMessage) bool {
	return len(value) != 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
