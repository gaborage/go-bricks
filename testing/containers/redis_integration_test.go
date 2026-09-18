//go:build integration

package containers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveAnnounceIPAcceptsOnlyIPv4 pins the IPv4-only contract the cluster
// announce pair rests on: cluster-announce-ip is the address a host-side client
// dials back through the fixture's v4 port mapping, so an IPv6 literal is refused
// at this seam rather than published into a slot map whose redirect would be
// unreachable. An IPv4-mapped v6 literal is announced in its dotted form, the only
// spelling Redis takes. No Docker involved: the function is host-side string work
// plus a resolver lookup, and localhost answers from the hosts file.
func TestResolveAnnounceIPAcceptsOnlyIPv4(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		want    string
		wantErr string
	}{
		{name: "ipv4_literal_passes_through", host: "192.0.2.10", want: "192.0.2.10"},
		{name: "ipv4_mapped_v6_literal_is_dotted", host: "::ffff:192.0.2.10", want: "192.0.2.10"},
		{name: "ipv6_literal_is_refused", host: "::1", wantErr: "IPv6 literal"},
		{name: "hostname_resolves_to_ipv4", host: "localhost", want: "127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAnnounceIP(t.Context(), tt.host)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
