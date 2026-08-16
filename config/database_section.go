package config

// dbPlacement is where a database section sits in the configuration tree. It
// decides whether the section may be absent, whether a manager block is
// allowed, and how its errors are addressed.
type dbPlacement int

const (
	dbPlacementRoot   dbPlacement = iota // database — may be absent (ADR-047)
	dbPlacementNamed                     // databases.<name>
	dbPlacementTenant                    // multitenant.tenants.<id>.database
)

// dbStrictness is how normalization treats what a loaded configuration must
// state. Startup fails fast on identity gaps and on an explicit type that
// contradicts the connectionstring scheme; connect infers what it can, enforces
// the vendor rules that would otherwise fail silently open, fills defaults, and
// leaves identity to the dial (ADR-050, "the seam stays asymmetric by design").
type dbStrictness int

const (
	dbStrictnessStartup dbStrictness = iota
	dbStrictnessConnect
)

// dbSection names one database section: its path in the tree and its placement.
type dbSection struct {
	path      string
	placement dbPlacement
}

func rootDatabaseSection() dbSection {
	return dbSection{path: fieldDatabase, placement: dbPlacementRoot}
}

func namedDatabaseSection(name string) dbSection {
	return dbSection{path: "databases." + name, placement: dbPlacementNamed}
}

func tenantDatabaseSection(id string) dbSection {
	return dbSection{path: "multitenant.tenants." + id + ".database", placement: dbPlacementTenant}
}
