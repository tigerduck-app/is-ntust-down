package probe

import "testing"

func TestDeriveServiceState(t *testing.T) {
	tests := []struct {
		name   string
		states []State
		want   State
	}{
		{"no checks", nil, StateUnknown},
		{"all up", []State{StateUp, StateUp}, StateUp},
		{"all down", []State{StateDown, StateDown}, StateDown},
		{"site up, sso down is degraded", []State{StateUp, StateDown}, StateDegraded},
		{"explicit degraded check", []State{StateUp, StateDegraded}, StateDegraded},
		{"paused sso does not drag a healthy site down", []State{StateUp, StateUnknown}, StateUp},
		{"paused sso does not rescue a dead site", []State{StateDown, StateUnknown}, StateDown},
		{"nothing known", []State{StateUnknown, StateUnknown}, StateUnknown},
		{"single retired check", []State{StateRetired}, StateRetired},
		{"retired ignored beside a live check", []State{StateRetired, StateUp}, StateUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveServiceState(tt.states); got != tt.want {
				t.Errorf("DeriveServiceState(%v) = %q, want %q", tt.states, got, tt.want)
			}
		})
	}
}
