package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeAndCheckResolver runs both halves of the resolver split in phase
// order, for tables whose cases need a fill before the rejection they pin.
func normalizeAndCheckResolver(cfg *ResolverConfig) error {
	normalizeMultitenantResolver(cfg)
	return checkMultitenantResolver(cfg)
}

func TestValidateMultitenantResolver(t *testing.T) {
	tests := []struct {
		name           string
		config         ResolverConfig
		expectError    bool
		errorContains  string
		expectedHeader string // Check default header is set
		expectedDomain string // Check the domain was normalized
	}{
		{
			name: "valid_header_resolver",
			config: ResolverConfig{
				Type:   "header",
				Header: "X-Custom-Tenant",
			},
			expectError: false,
		},
		{
			name: "header_resolver_gets_default_header",
			config: ResolverConfig{
				Type: "header",
				// No header specified, should get default
			},
			expectError:    false,
			expectedHeader: testTenantHeader,
		},
		{
			name: "valid_subdomain_resolver",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: testDomain,
			},
			expectError: false,
		},
		{
			name: "valid_composite_resolver",
			config: ResolverConfig{
				Type:    "composite",
				Header:  testTenantHeader,
				Domain:  testDomain,
				Proxies: true,
				Order:   []string{ResolverTypeSubdomain, ResolverTypeHeader},
			},
			expectError: false,
		},
		{
			name: "invalid_resolver_type",
			config: ResolverConfig{
				Type: "invalid",
			},
			expectError:   true,
			errorContains: "multitenant.resolver.type",
		},
		{
			name: "subdomain_missing_domain",
			config: ResolverConfig{
				Type: "subdomain",
				// Missing domain
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "subdomain_domain_without_leading_dot",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: "api.example.com",
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "subdomain_domain_with_surrounding_whitespace",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: "  api.example.com\t",
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "subdomain_domain_dot_only_rejected",
			config: ResolverConfig{
				Type:   "subdomain",
				Domain: ".", // Strips to "" after trimming the leading dot — newSubdomainResolver would build nil
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "composite_missing_domain",
			config: ResolverConfig{
				Type:   "composite",
				Header: testTenantHeader,
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
				// Missing domain
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "composite_domain_without_leading_dot",
			config: ResolverConfig{
				Type:   "composite",
				Header: testTenantHeader,
				Domain: "api.example.com",
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
			},
			expectError:    false,
			expectedDomain: testDomain,
		},
		{
			name: "header_resolver_stray_domain_left_alone",
			config: ResolverConfig{
				Type:   "header",
				Domain: "api.example.com",
			},
			expectError:    false,
			expectedDomain: "api.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndCheckResolver(&tt.config)
			if tt.expectError {
				require.ErrorContains(t, err, tt.errorContains)
			} else {
				assert.NoError(t, err)
				// Check if default header was set
				if tt.expectedHeader != "" {
					assert.Equal(t, tt.expectedHeader, tt.config.Header)
				}
				if tt.expectedDomain != "" {
					assert.Equal(t, tt.expectedDomain, tt.config.Domain)
				}
			}
		})
	}
}

func TestResolverOrderValidationRejectsUnknown(t *testing.T) {
	tests := []struct {
		name          string
		config        ResolverConfig
		expectError   bool
		errorContains string
		expectedOrder []string
	}{
		{
			name: "unknown_entry_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{"bogus"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "duplicate_entry_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{"header", "header"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "order_on_non_composite_type_rejected",
			config: ResolverConfig{
				Type:  "header",
				Order: []string{"header"},
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "composite_without_order_is_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				// Order intentionally unset — composite requires an explicit order,
				// there is no implicit default (the framework can't know which
				// sub-resolvers are attacker-reachable vs. gateway-asserted).
			},
			expectError:   true,
			errorContains: "multitenant.resolver.order",
		},
		{
			name: "order_with_path_but_no_segment_rejected",
			config: ResolverConfig{
				Type:  "composite",
				Order: []string{ResolverTypePath, ResolverTypeHeader},
				// Path.Segment intentionally unset — order names "path" but the
				// path sub-resolver has no segment configured, so it would build
				// as nil and silently degrade the composite to header-only.
			},
			expectError:   true,
			errorContains: "multitenant.resolver.path.segment",
		},
		{
			name: "order_with_subdomain_and_dot_domain_rejected",
			config: ResolverConfig{
				Type:   "composite",
				Order:  []string{ResolverTypeSubdomain, ResolverTypeHeader},
				Domain: ".", // Strips to "" after trimming the leading dot — newSubdomainResolver would build nil
			},
			expectError:   true,
			errorContains: "multitenant.resolver.domain",
		},
		{
			name: "valid_configured_order_preserved",
			config: ResolverConfig{
				Type:   "composite",
				Domain: testDomain,
				Order:  []string{ResolverTypeHeader, ResolverTypeSubdomain},
			},
			expectError:   false,
			expectedOrder: []string{ResolverTypeHeader, ResolverTypeSubdomain},
		},
		{
			name: "order_excluding_subdomain_does_not_require_domain",
			config: ResolverConfig{
				Type:  "composite",
				Order: []string{ResolverTypePath, ResolverTypeHeader},
				Path:  PathResolverConfig{Segment: 1},
			},
			expectError:   false,
			expectedOrder: []string{ResolverTypePath, ResolverTypeHeader},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndCheckResolver(&tt.config)
			if tt.expectError {
				require.ErrorContains(t, err, tt.errorContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOrder, tt.config.Order)
		})
	}
}
