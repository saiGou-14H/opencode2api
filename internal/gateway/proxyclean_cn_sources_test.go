package gateway

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseChinaSourceActualSchemas(t *testing.T) {
	tests := []struct {
		id, body string
		want     []string
	}{
		{"xyzs-cn", `[
   {"country":"CN","host":"111.193.243.224","port":8888,"protocol":"https","proxy":"111.193.243.224:8888","checkType":"https_connect_probe","supportsHttps":true,"countrySource":"dbip"},
   {"country":"CN","host":"113.249.111.67","port":1080,"protocol":"socks5","checkType":"socks5_probe"},
   {"country":"CN","host":"223.85.21.195","port":8080,"protocol":"http","checkType":"http_probe"}
  ]`, []string{"http://111.193.243.224:8888", "socks5://113.249.111.67:1080", "http://223.85.21.195:8080"}},
		{"monosans-cn", `[
   {"protocol":"http","username":null,"password":null,"host":"49.233.205.10","port":3128,"exit_ip":"127.0.0.1","geolocation":{"country":{"iso_code":"CN"}}},
   {"protocol":"socks5","host":"112.5.33.179","port":999,"exit_ip":"8.8.8.8","geolocation":{"country":{"iso_code":"CN"}}},
   {"protocol":"https","host":"82.157.237.244","port":443,"geolocation":{"country":{"iso_code":"CN"}}}
  ]`, []string{"http://49.233.205.10:3128", "socks5://112.5.33.179:999", "https://82.157.237.244:443"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, err := parseChinaSource(tt.id, strings.NewReader(tt.body), 20)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestParseChinaSourceRejectCandidates(t *testing.T) {
	cases := []string{
		`{"country":"HK","host":"8.8.8.8","port":80,"protocol":"http"}`,
		`{"country":"MO","host":"8.8.8.8","port":80,"protocol":"http"}`,
		`{"country":"TW","host":"8.8.8.8","port":80,"protocol":"http"}`,
		`{"country":"cn","host":"8.8.8.8","port":80,"protocol":"http"}`,
		`{"country":"CN ","host":"8.8.8.8","port":80,"protocol":"http"}`,
		`{"country":"CN","host":"8.8.8.8","port":80,"protocol":"socks4"}`,
		`{"country":"CN","host":"8.8.8.8","port":80,"protocol":"https","supportsHttps":true}`,
		`{"country":"CN","host":"8.8.8.8","port":80,"protocol":"https","checkType":"tls_probe"}`,
		`{"country":"CN","host":"8.8.8.8","port":80,"protocol":"http","username":"user"}`,
		`{"country":"CN","host":"8.8.8.8","port":80,"protocol":"http","password":""}`,
		`{"country":"CN","host":"8.8.8.8","port":"80","protocol":"http"}`,
		`{"country":"CN","host":"8.8.8.8","port":65536,"protocol":"http"}`,
		`{"country":"CN","host":"8.8.8.8","port":0,"protocol":"http"}`,
		`"http://8.8.8.8:80"`, `null`, `[]`,
	}
	for _, host := range []string{"10.0.0.1", "127.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:8.8.8.8", "example.com", "user:pass@8.8.8.8", "8.8.8.8 #comment"} {
		cases = append(cases, `{"country":"CN","host":"`+host+`","port":80,"protocol":"http"}`)
	}
	for _, body := range cases {
		_, err := parseChinaSource("xyzs-cn", strings.NewReader("["+body+"]"), 10)
		if err == nil || err.Error() != "source_empty" {
			t.Errorf("accepted %s: %v", body, err)
		}
	}
	// Top-level or registered-country CN must not override the actual country.
	for _, country := range []string{"HK", "MO", "TW", "US", "cn"} {
		body := `[{"country":"CN","host":"8.8.8.8","port":80,"protocol":"http","exit_ip":"49.233.205.10","geolocation":{"country":{"iso_code":"` + country + `"},"registered_country":{"iso_code":"CN"}}}]`
		if _, err := parseChinaSource("monosans-cn", strings.NewReader(body), 10); err == nil {
			t.Fatal("wrong country admitted")
		}
	}
}

func TestParseChinaSourceLimitDedupMixed(t *testing.T) {
	row := `{"country":"CN","host":"8.8.8.8","port":80,"protocol":"http"}`
	other := `{"country":"CN","host":"1.1.1.1","port":80,"protocol":"http"}`
	body := `["socks5://8.8.8.8:1080",{},` + row + `,` + row + `,` + other + `]`
	for _, limit := range []int{1, 2} {
		got, err := parseChinaSource("xyzs-cn", strings.NewReader(body), limit)
		if err != nil || len(got) != limit {
			t.Fatalf("limit %d: %v %v", limit, got, err)
		}
	}
	if _, err := parseChinaSource("xyzs-cn", strings.NewReader(`[`+row+`,BROKEN]`), 1); err == nil {
		t.Fatal("ignored invalid tail after limit")
	}
}

type chinaSourceCountingReader struct{ remaining, read int }

func (r *chinaSourceCountingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = ' '
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

type chinaSourceErrorReader struct{}

func (chinaSourceErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("private transport detail")
}

func TestParseChinaSourceBoundsAndInvalid(t *testing.T) {
	row := `[{"country":"CN","host":"8.8.8.8","port":80,"protocol":"http"}]`
	exact := row + strings.Repeat(" ", chinaSourceMaxBytes-len(row))
	if got, err := parseChinaSource("xyzs-cn", strings.NewReader(exact), 1); err != nil || len(got) != 1 {
		t.Fatalf("exact cap: %v %v", got, err)
	}
	if _, err := parseChinaSource("xyzs-cn", strings.NewReader(exact+" "), 1); err == nil || err.Error() != "source_too_large" {
		t.Fatalf("cap+1: %v", err)
	}
	r := &chinaSourceCountingReader{remaining: chinaSourceMaxBytes * 2}
	if _, err := parseChinaSource("xyzs-cn", r, 1); err == nil || err.Error() != "source_too_large" {
		t.Fatal(err)
	}
	if r.read != chinaSourceMaxBytes+1 {
		t.Fatalf("read %d bytes", r.read)
	}
	for _, body := range []string{"", "null", "{}", "[] {}", "http://8.8.8.8:80", "[", row + " []"} {
		if _, err := parseChinaSource("xyzs-cn", strings.NewReader(body), 10); err == nil || err.Error() != "source_invalid" {
			t.Errorf("invalid %q: %v", body, err)
		}
	}
	if _, err := parseChinaSource("xyzs-cn", chinaSourceErrorReader{}, 1); err == nil || err.Error() != "source_invalid" {
		t.Fatal(err)
	}
	if _, err := parseChinaSource("unknown", strings.NewReader(row), 1); err == nil || err.Error() != "source_unknown" {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1} {
		if _, err := parseChinaSource("xyzs-cn", strings.NewReader(row), limit); err == nil {
			t.Fatal("invalid limit")
		}
	}
	if _, err := parseChinaSource("xyzs-cn", nil, 1); err == nil {
		t.Fatal("nil reader")
	}
}
