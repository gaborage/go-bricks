package config

import (
	"fmt"
	"slices"
	"strings"
)

// normalizeMultitenantResolver fills the header default and, when the config
// builds a subdomain resolver, trims a delivered domain and prefixes it with
// '.'. An empty domain stays empty for checkMultitenantResolver to reject.
func normalizeMultitenantResolver(cfg *ResolverConfig) {
	if cfg.Header == "" {
		cfg.Header = "X-Tenant-ID"
	}
	if buildsSubdomainResolver(cfg) && domainDelivered(cfg.Domain) {
		cfg.Domain = strings.TrimSpace(cfg.Domain)
		if !strings.HasPrefix(cfg.Domain, ".") {
			cfg.Domain = "." + cfg.Domain
		}
	}
}

// checkMultitenantResolver rejects an unknown resolver type, a composite order
// that is missing or malformed, and the field requirements each type implies
// (subdomain root domain, path prefix/segment).
func checkMultitenantResolver(cfg *ResolverConfig) error {
	validTypes := []string{ResolverTypeHeader, ResolverTypeSubdomain, ResolverTypePath, ResolverTypeComposite}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("multitenant.resolver.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}

	if err := validateResolverOrder(cfg); err != nil {
		return err
	}

	if err := validateSubdomainResolverFields(cfg); err != nil {
		return err
	}
	return validatePathResolverFields(cfg)
}

// validateResolverOrder validates the composite sub-resolver order. Order is
// only meaningful for type: composite — setting it on any other type is
// rejected rather than silently ignored. For type: composite, Order is
// REQUIRED — there is no implicit default. Any default (header-last or
// header-first) is an unverifiable bet on the deployment's edge topology, and
// a caller-controlled X-Tenant-ID header must never silently outrank a
// resolver the operator explicitly wired up. See DefaultResolverOrder.
func validateResolverOrder(cfg *ResolverConfig) error {
	if cfg.Type != ResolverTypeComposite {
		if len(cfg.Order) > 0 {
			return NewValidationError(fieldResolverOrder, "only valid when multitenant.resolver.type is 'composite'")
		}
		return nil
	}

	if len(cfg.Order) == 0 {
		err := NewMissingFieldError(fieldResolverOrder, "MULTITENANT_RESOLVER_ORDER", fieldResolverOrder)
		err.Message = "required when multitenant.resolver.type is 'composite' — no implicit default"
		err.Details = []string{
			"recommended: [subdomain, path, header]",
			"if a trusted gateway strips and sets X-Tenant-ID, use a header-first order instead, e.g. [header, subdomain, path]",
		}
		return err
	}

	seen := make(map[string]bool, len(cfg.Order))
	for _, entry := range cfg.Order {
		if !slices.Contains(resolverOrderEntries, entry) {
			return NewInvalidFieldError(fieldResolverOrder, fmt.Sprintf(errNotSupportedFmt, entry), resolverOrderEntries)
		}
		if seen[entry] {
			return NewValidationError(fieldResolverOrder, fmt.Sprintf("duplicate entry %q", entry))
		}
		seen[entry] = true
	}
	return nil
}

func validateSubdomainResolverFields(cfg *ResolverConfig) error {
	if !buildsSubdomainResolver(cfg) {
		return nil
	}
	if !domainDelivered(cfg.Domain) {
		return NewMissingFieldError("multitenant.resolver.domain", "MULTITENANT_RESOLVER_DOMAIN", "multitenant.resolver.domain")
	}
	return nil
}

// buildsSubdomainResolver reports whether the config will construct a
// subdomain resolver: type subdomain, or a composite whose order includes
// one. Order is required and checked separately, so a composite reaching
// the check always has an explicit, non-empty Order.
func buildsSubdomainResolver(cfg *ResolverConfig) bool {
	switch cfg.Type {
	case ResolverTypeSubdomain:
		return true
	case ResolverTypeComposite:
		return slices.Contains(cfg.Order, ResolverTypeSubdomain)
	default:
		return false
	}
}

// domainDelivered treats "." and whitespace as no domain: once the leading dot
// is trimmed nothing is left, and newSubdomainResolver builds nil from that.
func domainDelivered(domain string) bool {
	return strings.TrimPrefix(strings.TrimSpace(domain), ".") != ""
}

// validatePathResolverFields enforces path-segment + prefix rules for the path
// resolver and for composite configurations that opt into a path sub-resolver
// (cfg.Order containing "path" indicates intent to include path — Order is
// required and validated before this runs, so it is always explicit here).
func validatePathResolverFields(cfg *ResolverConfig) error {
	required := cfg.Type == ResolverTypePath ||
		(cfg.Type == ResolverTypeComposite && slices.Contains(cfg.Order, ResolverTypePath))
	if !required {
		return nil
	}
	if cfg.Path.Segment <= 0 {
		return NewValidationError("multitenant.resolver.path.segment", errMustBePositive)
	}
	if cfg.Path.Prefix != "" && !strings.HasPrefix(cfg.Path.Prefix, "/") {
		return NewValidationError("multitenant.resolver.path.prefix", "must start with '/' when set")
	}
	return nil
}
