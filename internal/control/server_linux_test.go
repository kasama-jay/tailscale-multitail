//go:build linux

package control

import (
	"strings"
	"testing"
)

func TestValidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want bool
	}{
		{
			name: "status",
			req:  Request{Op: "status"},
			want: true,
		},
		{
			name: "login with key",
			req:  Request{Op: "login", AuthKey: "tskey-auth-test"},
			want: true,
		},
		{
			name: "key on status",
			req:  Request{Op: "status", AuthKey: "secret"},
			want: false,
		},
		{
			name: "config on login",
			req:  Request{Op: "login", ConfigYAML: "version: 1"},
			want: false,
		},
		{
			name: "large key",
			req:  Request{Op: "login", AuthKey: strings.Repeat("x", (16<<10)+1)},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validRequest(test.req); got != test.want {
				t.Fatalf("validRequest(%+v) = %v, want %v", test.req, got, test.want)
			}
		})
	}
}
