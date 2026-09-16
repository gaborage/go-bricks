package config

import (
	"net/url"
	"slices"
	"strings"
)

// pgSSLEnvKey pairs a libpq PGSSL* environment variable with its DSN keyword.
// pgx's parseEnvSettings maps these five (skipping empty values) and mergeSettings
// copies DSN over env, so a present DSN key — empty included — shadows the variable.
type pgSSLEnvKey struct {
	env string
	dsn string
}

const (
	pgDSNSSLMode        = "sslmode"
	pgDSNSSLRootCert    = "sslrootcert"
	pgDSNSSLCert        = "sslcert"
	pgDSNSSLKey         = "sslkey"
	pgDSNSSLNegotiation = "sslnegotiation"
	// pgDSNSSLAlias is the URI-only spelling pgx rewrites into sslmode=require.
	pgDSNSSLAlias = "ssl"
)

// pgSSLEnvKeys is the production table of TLS-claim env names. Hermetic test
// helpers reuse it so a newly judged variable cannot slip past the scrub.
var pgSSLEnvKeys = []pgSSLEnvKey{
	{env: "PGSSLMODE", dsn: pgDSNSSLMode},
	{env: "PGSSLROOTCERT", dsn: pgDSNSSLRootCert},
	{env: "PGSSLCERT", dsn: pgDSNSSLCert},
	{env: "PGSSLKEY", dsn: pgDSNSSLKey},
	{env: "PGSSLNEGOTIATION", dsn: pgDSNSSLNegotiation},
}

// pgDSNSetting is one DSN key's presence and value; set includes an empty value, which shadows env.
type pgDSNSetting struct {
	set   bool
	value string
}

// pgDSNTLSKeys is the five TLS keys scanPostgresDSN records presence for, parallel to pgSSLEnvKeys.
type pgDSNTLSKeys struct {
	sslmode        pgDSNSetting
	sslrootcert    pgDSNSetting
	sslcert        pgDSNSetting
	sslkey         pgDSNSetting
	sslnegotiation pgDSNSetting
}

// pgDSNScan is a connection string's own host and TLS-key presence; hostSet includes an empty host, which shadows PGHOST.
type pgDSNScan struct {
	hostSet bool
	host    string
	tls     pgDSNTLSKeys
	// sslAlias records that sslmode was written by the ssl=true rewrite rather than by the DSN.
	sslAlias bool
}

// claimSource is the key the DSN TEXT carries for a merged TLS key, so the refusal
// names something the operator can find in the string: a sslmode pgx rewrote from
// the URI ssl=true alias is reported as ssl.
func (s *pgDSNScan) claimSource(dsnKey string) string {
	if dsnKey == pgDSNSSLMode && s.sslAlias {
		return pgDSNSSLAlias
	}
	return dsnKey
}

func pgDSNSettingOf(settings map[string]string, key string) pgDSNSetting {
	value, set := settings[key]
	return pgDSNSetting{set: set, value: value}
}

func pgDSNTLSKeysFrom(settings map[string]string) pgDSNTLSKeys {
	return pgDSNTLSKeys{
		sslmode:        pgDSNSettingOf(settings, pgDSNSSLMode),
		sslrootcert:    pgDSNSettingOf(settings, pgDSNSSLRootCert),
		sslcert:        pgDSNSettingOf(settings, pgDSNSSLCert),
		sslkey:         pgDSNSettingOf(settings, pgDSNSSLKey),
		sslnegotiation: pgDSNSettingOf(settings, pgDSNSSLNegotiation),
	}
}

func (t *pgDSNTLSKeys) setting(dsn string) pgDSNSetting {
	switch dsn {
	case pgDSNSSLMode:
		return t.sslmode
	case pgDSNSSLRootCert:
		return t.sslrootcert
	case pgDSNSSLCert:
		return t.sslcert
	case pgDSNSSLKey:
		return t.sslkey
	case pgDSNSSLNegotiation:
		return t.sslnegotiation
	default:
		return pgDSNSetting{}
	}
}

// pgTLSKeyClaims is the unchanged [C65.2] rule 2 claim test, applied to one merged key.
func pgTLSKeyClaims(dsnKey, value string) bool {
	switch dsnKey {
	case pgDSNSSLMode:
		return slices.Contains(pgTLSMandatorySSLModes, value)
	case pgDSNSSLNegotiation:
		return value == "direct"
	default:
		return value != ""
	}
}

// dsnClaimsTLS is the DSN-text claim, with no environment: scanner tests stay hermetic.
func (s *pgDSNScan) dsnClaimsTLS() bool {
	for _, k := range pgSSLEnvKeys {
		st := s.tls.setting(k.dsn)
		if st.set && pgTLSKeyClaims(k.dsn, st.value) {
			return true
		}
	}
	return false
}

// scanPostgresDSN mirrors pgx v5 pgconn ParseConfig's tokenizers, without its file and environment reads.
func scanPostgresDSN(cs string) (pgDSNScan, bool) {
	if strings.Contains(cs, "\x00") {
		return pgDSNScan{}, false
	}
	var settings map[string]string
	var aliased, ok bool
	if body, isURI := pgURIBody(cs); isURI {
		settings, aliased, ok = pgURISettings(body)
	} else {
		settings, ok = pgKeywordSettings(cs)
	}
	if !ok {
		return pgDSNScan{}, false
	}
	host, hostSet := settings["host"]
	return pgDSNScan{
		hostSet:  hostSet,
		host:     host,
		tls:      pgDSNTLSKeysFrom(settings),
		sslAlias: aliased,
	}, true
}

// pgURIBody mirrors pgx v5 ParseConfigWithOptions' exact, case-sensitive URI prefix test.
func pgURIBody(cs string) (string, bool) {
	if body, ok := strings.CutPrefix(cs, "postgresql://"); ok {
		return body, true
	}
	return strings.CutPrefix(cs, "postgres://")
}

// pgURISettings mirrors pgx v5 pgconn parseURLSettings for the host and query settings.
func pgURISettings(p string) (settings map[string]string, aliased, ok bool) {
	settings = make(map[string]string)
	if i := strings.IndexAny(p, "@/"); i >= 0 && p[i] == '@' {
		p = p[i+1:]
	}
	hosts, p, ok := pgURIHosts(p)
	if !ok {
		return nil, false, false
	}
	if hosts != "" {
		host, ok := pgURIDecode(hosts)
		if !ok {
			return nil, false, false
		}
		settings["host"] = host
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		aliased, ok = pgURIQuery(p[i+1:], settings)
		return settings, aliased, ok
	}
	return settings, false, true
}

// pgURIHosts returns the raw comma-joined host list of a URI authority and the unread rest.
func pgURIHosts(p string) (hosts, rest string, ok bool) {
	var b strings.Builder
	for {
		if strings.HasPrefix(p, "[") {
			end := strings.IndexByte(p, ']')
			if end <= 1 {
				return "", "", false
			}
			b.WriteString(p[1:end])
			p = p[end+1:]
			if pgIndexAnyOrLen(p, ":/?,") != 0 {
				return "", "", false
			}
		} else {
			i := pgIndexAnyOrLen(p, ":/?,")
			b.WriteString(p[:i])
			p = p[i:]
		}
		if strings.HasPrefix(p, ":") {
			p = p[pgIndexAnyOrLen(p, "/?,"):]
		}
		if !strings.HasPrefix(p, ",") {
			return b.String(), p, true
		}
		p = p[1:]
		b.WriteByte(',')
	}
}

// pgIndexAnyOrLen is strings.IndexAny with "not found" meaning the whole string.
func pgIndexAnyOrLen(s, chars string) int {
	if i := strings.IndexAny(s, chars); i >= 0 {
		return i
	}
	return len(s)
}

// pgURIQuery mirrors pgx v5 pgconn parseURLQueryParams.
func pgURIQuery(params string, settings map[string]string) (aliased, ok bool) {
	sslWasLast := false
	for params != "" {
		var pair string
		pair, params, _ = strings.Cut(params, "&")
		rawKey, rawValue, found := strings.Cut(pair, "=")
		if !found || strings.Contains(rawValue, "=") {
			return false, false
		}
		key, keyOK := pgURIDecode(rawKey)
		value, valueOK := pgURIDecode(rawValue)
		if !keyOK || !valueOK {
			return false, false
		}
		switch key {
		case pgDSNSSLAlias:
			sslWasLast = true
		case pgDSNSSLMode:
			sslWasLast = false
		}
		settings[key] = value
	}
	if sslWasLast && settings[pgDSNSSLAlias] == "true" {
		settings[pgDSNSSLMode] = sslModeRequire
		return true, true
	}
	return false, true
}

// pgURIDecode mirrors pgx v5 pgconn uriDecode.
func pgURIDecode(raw string) (string, bool) {
	raw = strings.Trim(raw, " ")
	decoded, err := url.PathUnescape(raw)
	return decoded, err == nil && !strings.Contains(raw, " ") && !strings.Contains(decoded, "\x00")
}

const pgKeywordSpace = " \t\n\r\v\f"

// pgKeywordSettings mirrors pgx v5 pgconn parseKeywordValueSettings.
func pgKeywordSettings(s string) (map[string]string, bool) {
	settings := make(map[string]string)
	s = strings.TrimLeft(s, pgKeywordSpace)
	for s != "" {
		key, rest, found := strings.Cut(s, "=")
		key = strings.Trim(key, pgKeywordSpace)
		if !found || key == "" || strings.ContainsAny(key, pgKeywordSpace) {
			return nil, false
		}
		val, rest, ok := pgKeywordValue(strings.TrimLeft(rest, pgKeywordSpace))
		if !ok {
			return nil, false
		}
		settings[key] = val
		s = strings.TrimLeft(rest, pgKeywordSpace)
	}
	return settings, true
}

// pgKeywordValue reads one value, quoted or not, and returns it unescaped with the unread rest.
func pgKeywordValue(s string) (val, rest string, ok bool) {
	quoted := strings.HasPrefix(s, "'")
	if quoted {
		s = s[1:]
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\'':
			return sb.String(), s[i+1:], true
		case !quoted && strings.IndexByte(pgKeywordSpace, c) >= 0:
			return sb.String(), s[i:], true
		case c == '\\':
			i++
		}
		if i < len(s) {
			sb.WriteByte(s[i])
		}
	}
	return sb.String(), "", !quoted
}
