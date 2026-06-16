package postgresql

// boolPtr returns a pointer to b, for constructing config structs with optional
// *bool fields (e.g. config.PoolKeepAliveConfig.Enabled).
func boolPtr(b bool) *bool {
	return &b
}
