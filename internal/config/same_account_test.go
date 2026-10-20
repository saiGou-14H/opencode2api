package config

import "testing"

func TestSameAccountStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
		want  []int
	}{
		{"", true, []int{401, 403, 429}}, {"  ", true, []int{401, 403, 429}},
		{" 100, 200,403,599,403 ", true, []int{100, 200, 403, 599}},
		{"99", false, nil}, {"600", false, nil}, {"401,", false, nil}, {",403", false, nil},
		{"401,,403", false, nil}, {"401 403", false, nil}, {"foo", false, nil}, {"+401", false, nil},
	} {
		got, err := (RetryConfig{SameAccountStatusCodes: tc.raw}).SameAccountStatuses()
		if (err == nil) != tc.valid {
			t.Errorf("%q valid=%v err=%v", tc.raw, tc.valid, err)
			continue
		}
		if err != nil {
			continue
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%q got=%v", tc.raw, got)
		}
		for _, code := range tc.want {
			if !got[code] {
				t.Errorf("%q missing %d", tc.raw, code)
			}
		}
	}
}
