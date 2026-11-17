package syntax

import "testing"

func TestNormalizeTemporal(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"14:00", true},
		{"1979-05-27T07:32", true},
		{"1979-05-27t07:32z", true},
		{"1979-05-27 07:32-07:00", true},
		{"14:00:01.123", true},
		{"1979-05-27", true},
		{"14:00.5", false},
		{"24:00", false},
		{"14:60", false},
		{"1979-05-27T07:32+24:00", false},
		{"1979-05-27T07:32+00:60", false},
		{"1979-02-30T07:32", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := NormalizeValue(Temporal(tc.value))
			if tc.valid {
				if err != nil || got != Temporal(tc.value) {
					t.Fatalf("got %v, %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid temporal value")
			}
		})
	}
}
