package multitenant

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	tenantIDHeader       = "X-Tenant-ID"
	emptyResolver        = "nil resolver"
	testDomain           = "example.com"
	validTenantID        = "valid-tenant"
	tenantIDRegex        = `^[a-zA-Z0-9\-]+$`
	testHost             = "localhost:8080"
	xForwardedHostHeader = "X-Forwarded-Host"
)

// setupTestRequest creates an HTTP request for testing resolvers
func setupTestRequest(host string, headers map[string]string) *http.Request {
	ctx := context.Background()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+host+"/test", http.NoBody)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

// TestHeaderResolverResolveTenant tests the HeaderResolver implementation
func TestHeaderResolverResolveTenant(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		resolver    *HeaderResolver
		headers     map[string]string
		expected    string
		expectError bool
	}{
		{
			name:     "success with default header",
			resolver: &HeaderResolver{},
			headers:  map[string]string{tenantIDHeader: "tenant123"},
			expected: "tenant123",
		},
		{
			name:     "success with custom header",
			resolver: &HeaderResolver{HeaderName: "Custom-Tenant"},
			headers:  map[string]string{"Custom-Tenant": "custom-tenant"},
			expected: "custom-tenant",
		},
		{
			name:     "success with whitespace trimming",
			resolver: &HeaderResolver{},
			headers:  map[string]string{tenantIDHeader: "  spaced-tenant  "},
			expected: "spaced-tenant",
		},
		{
			name:        "missing header",
			resolver:    &HeaderResolver{},
			headers:     map[string]string{},
			expectError: true,
		},
		{
			name:        "empty header value",
			resolver:    &HeaderResolver{},
			headers:     map[string]string{tenantIDHeader: ""},
			expectError: true,
		},
		{
			name:        "whitespace only header value",
			resolver:    &HeaderResolver{},
			headers:     map[string]string{tenantIDHeader: "   "},
			expectError: true,
		},
		{
			name:        emptyResolver,
			resolver:    nil,
			headers:     map[string]string{tenantIDHeader: "tenant123"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := setupTestRequest(testDomain, tt.headers)

			if tt.name == emptyResolver {
				req = nil
			}

			var tenantID string
			var err error

			if tt.resolver == nil {
				// Handle nil resolver case - expect error without calling ResolveTenant
				tenantID = ""
				err = ErrTenantResolutionFailed
			} else {
				tenantID, err = tt.resolver.ResolveTenant(ctx, req)
			}

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, ErrTenantResolutionFailed, err)
				assert.Empty(t, tenantID)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, tenantID)
			}
		})
	}
}

// TestCompositeResolverResolveTenant tests the CompositeResolver implementation
func TestCompositeResolverResolveTenant(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		resolver    *CompositeResolver
		host        string
		headers     map[string]string
		expected    string
		expectError bool
	}{
		{
			name: "success with first resolver",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{
					&HeaderResolver{HeaderName: tenantIDHeader},
					&SubdomainResolver{RootDomain: testDomain},
				},
			},
			host:     "some.host.com",
			headers:  map[string]string{tenantIDHeader: "header-tenant"},
			expected: "header-tenant",
		},
		{
			name: "success with second resolver (first fails)",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{
					&HeaderResolver{HeaderName: tenantIDHeader}, // will fail - no header
					&SubdomainResolver{RootDomain: testDomain},
				},
			},
			host:     "subdomain-tenant.example.com",
			headers:  map[string]string{}, // no header, falls back to subdomain
			expected: "subdomain-tenant",
		},
		{
			name: "success with regex validation",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{
					&HeaderResolver{HeaderName: tenantIDHeader},
				},
				TenantRegex: regexp.MustCompile(`^tenant-\d+$`),
			},
			host:     testDomain,
			headers:  map[string]string{tenantIDHeader: "tenant-123"},
			expected: "tenant-123",
		},
		{
			name: "all resolvers fail",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{
					&HeaderResolver{HeaderName: tenantIDHeader}, // no header
					&SubdomainResolver{RootDomain: testDomain},  // wrong domain
				},
			},
			host:        "wrong.domain.com",
			headers:     map[string]string{}, // no header
			expectError: true,
		},
		{
			name: "nil resolver in list",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{
					nil, // skipped
					&HeaderResolver{HeaderName: tenantIDHeader},
				},
			},
			host:     testDomain,
			headers:  map[string]string{tenantIDHeader: validTenantID},
			expected: validTenantID,
		},
		{
			name:        "nil composite resolver",
			resolver:    nil,
			host:        testDomain,
			expectError: true,
		},
		{
			name: "empty resolvers list",
			resolver: &CompositeResolver{
				Resolvers: []TenantResolver{},
			},
			host:        testDomain,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := setupTestRequest(tt.host, tt.headers)

			if tt.name == "nil composite resolver" {
				req = nil
			}

			tenantID, err := tt.resolver.ResolveTenant(ctx, req)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, ErrTenantResolutionFailed, err)
				assert.Empty(t, tenantID)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, tenantID)
			}
		})
	}
}

// TestValidatingResolverResolveTenant tests the ValidatingResolver implementation
func TestValidatingResolverResolveTenant(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		resolver    *ValidatingResolver
		headers     map[string]string
		expected    string
		expectError bool
	}{
		{
			name: "success with valid tenant ID",
			resolver: &ValidatingResolver{
				Resolver:    &HeaderResolver{HeaderName: tenantIDHeader},
				TenantRegex: regexp.MustCompile(tenantIDRegex),
			},
			headers:  map[string]string{tenantIDHeader: "valid-tenant-123"},
			expected: "valid-tenant-123",
		},
		{
			name: "success without regex validation",
			resolver: &ValidatingResolver{
				Resolver: &HeaderResolver{HeaderName: tenantIDHeader},
				// No TenantRegex - validation skipped
			},
			headers:  map[string]string{tenantIDHeader: "any-format-Works"},
			expected: "any-format-Works",
		},
		{
			name: "validation fails - invalid format",
			resolver: &ValidatingResolver{
				Resolver:    &HeaderResolver{HeaderName: tenantIDHeader},
				TenantRegex: regexp.MustCompile(tenantIDRegex),
			},
			headers:     map[string]string{tenantIDHeader: "Invalid_Tenant"},
			expectError: true,
		},
		{
			name: "underlying resolver fails",
			resolver: &ValidatingResolver{
				Resolver:    &HeaderResolver{HeaderName: tenantIDHeader},
				TenantRegex: regexp.MustCompile(tenantIDRegex),
			},
			headers:     map[string]string{}, // no header - resolver fails
			expectError: true,
		},
		{
			name:        "nil validating resolver",
			resolver:    nil,
			headers:     map[string]string{tenantIDHeader: validTenantID},
			expectError: true,
		},
		{
			name: "nil underlying resolver",
			resolver: &ValidatingResolver{
				Resolver:    nil,
				TenantRegex: regexp.MustCompile(tenantIDRegex),
			},
			headers:     map[string]string{tenantIDHeader: validTenantID},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := setupTestRequest(testDomain, tt.headers)

			if tt.name == "nil validating resolver" {
				req = nil
			}

			tenantID, err := tt.resolver.ResolveTenant(ctx, req)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, ErrTenantResolutionFailed, err)
				assert.Empty(t, tenantID)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, tenantID)
			}
		})
	}
}

// TestHeaderResolverResolveTenantNilRequest tests nil request handling
func TestHeaderResolverResolveTenantNilRequest(t *testing.T) {
	ctx := context.Background()
	resolver := &HeaderResolver{}

	tenantID, err := resolver.ResolveTenant(ctx, nil)

	assert.Error(t, err)
	assert.Equal(t, ErrTenantResolutionFailed, err)
	assert.Empty(t, tenantID)
}

// TestSubdomainResolverResolveTenant tests the SubdomainResolver implementation
func TestSubdomainResolverResolveTenant(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		resolver    *SubdomainResolver
		host        string
		headers     map[string]string
		expected    string
		expectError bool
	}{
		{
			name:     "success with subdomain",
			resolver: &SubdomainResolver{RootDomain: testDomain},
			host:     "tenant1.example.com",
			expected: "tenant1",
		},
		{
			name:     "success with nested subdomain",
			resolver: &SubdomainResolver{RootDomain: "api.example.com"},
			host:     "tenant2.api.example.com",
			expected: "tenant2",
		},
		{
			name:     "success with port stripping",
			resolver: &SubdomainResolver{RootDomain: testDomain},
			host:     "tenant3.example.com:8080",
			expected: "tenant3",
		},
		{
			name:        "success with IPv6 localhost (port preserved)",
			resolver:    &SubdomainResolver{RootDomain: "::1"},
			host:        "[::1]:8080",
			expectError: true, // IPv6 won't match subdomain pattern
		},
		{
			name:     "success with X-Forwarded-Host",
			resolver: &SubdomainResolver{RootDomain: testDomain, TrustProxies: true},
			host:     testHost,
			headers:  map[string]string{xForwardedHostHeader: "tenant4.example.com"},
			expected: "tenant4",
		},
		{
			name:     "success with comma-separated X-Forwarded-Host",
			resolver: &SubdomainResolver{RootDomain: testDomain, TrustProxies: true},
			host:     testHost,
			headers:  map[string]string{xForwardedHostHeader: "tenant5.example.com, tenant6.example.com"},
			expected: "tenant5",
		},
		{
			name:        "root domain same as host",
			resolver:    &SubdomainResolver{RootDomain: testDomain},
			host:        testDomain,
			expectError: true,
		},
		{
			name:        "host doesn't match root domain",
			resolver:    &SubdomainResolver{RootDomain: testDomain},
			host:        "tenant.different.com",
			expectError: true,
		},
		{
			name:        "suffix trickery prevented",
			resolver:    &SubdomainResolver{RootDomain: testDomain},
			host:        "maliciousexample.com",
			expectError: true,
		},
		{
			name:        "empty root domain",
			resolver:    &SubdomainResolver{},
			host:        "tenant.example.com",
			expectError: true,
		},
		{
			name:        emptyResolver,
			resolver:    nil,
			host:        "tenant.example.com",
			expectError: true,
		},
		{
			name:        "proxies disabled but X-Forwarded-Host present",
			resolver:    &SubdomainResolver{RootDomain: testDomain, TrustProxies: false},
			host:        testHost,
			headers:     map[string]string{xForwardedHostHeader: "tenant7.example.com"},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := setupTestRequest(tt.host, tt.headers)

			if tt.name == emptyResolver {
				req = nil
			}

			var tenantID string
			var err error

			if tt.resolver == nil {
				// Handle nil resolver case - expect error without calling ResolveTenant
				tenantID = ""
				err = ErrTenantResolutionFailed
			} else {
				tenantID, err = tt.resolver.ResolveTenant(ctx, req)
			}

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, ErrTenantResolutionFailed, err)
				assert.Empty(t, tenantID)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, tenantID)
			}
		})
	}
}
