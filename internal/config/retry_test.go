package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRetryBackoffValidation(t *testing.T) {
	for _, tc := range []struct {
		name, retry string
		valid       bool
	}{
		{"legacy", ``, true},
		{"disabled", `,"backoff_initial_ms":0,"backoff_max_ms":0`, true},
		{"exponential", `,"backoff_initial_ms":100,"backoff_max_ms":1000`, true},
		{"fixed", `,"backoff_initial_ms":100,"backoff_max_ms":0`, true},
		{"negative", `,"backoff_initial_ms":-1`, false},
		{"negative max", `,"backoff_max_ms":-1`, false},
		{"max below initial", `,"backoff_initial_ms":100,"backoff_max_ms":99`, false},
		{"too large", `,"backoff_initial_ms":86400001`, false},
		{"max too large", `,"backoff_max_ms":86400001`, false},
		{"no attempts", `,"max_attempts":0`, false},
		{"no deadline", `,"timeout_seconds":0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			body := fmt.Sprintf(`{"server_keys":["local"],"anonymous":true,"retry":{"max_attempts":3,"timeout_seconds":300%s}}`, tc.retry)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.name == "legacy" && (cfg.Retry.BackoffInitialMS != 0 || cfg.Retry.BackoffMaxMS != 0) {
				t.Fatal("legacy backoff must remain disabled")
			}
		})
	}
}
