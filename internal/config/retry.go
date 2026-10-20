package config

import (
	"fmt"
	"strconv"
	"strings"
)

// SameAccountStatuses parses the explicit status-code policy. A successful
// status is legal but potentially bills/generates twice; callers must only
// apply this policy before any response is released downstream.
func (cfg RetryConfig) SameAccountStatuses() (map[int]bool, error) {
	raw := strings.TrimSpace(cfg.SameAccountStatusCodes)
	if raw == "" {
		raw = "401,403,429"
	}
	codes := make(map[int]bool)
	for _, token := range strings.Split(raw, ",") {
		token = strings.TrimSpace(token)
		code, err := strconv.Atoi(token)
		if err != nil || len(token) != 3 || code < 100 || code > 599 {
			return nil, fmt.Errorf("retry.same_account_status_codes must contain comma-separated codes from 100 through 599")
		}
		codes[code] = true
	}
	return codes, nil
}
