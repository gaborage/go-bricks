package config

import (
	"net/url"
	"slices"
	"strings"
)

// pgDSNScan is a connection string's own host and TLS claim; hostSet includes an empty host, which shadows PGHOST.
type pgDSNScan struct {
	hostSet   bool
	host      string
	claimsTLS bool
}

// scanPostgresDSN mirrors pgx v5 pgconn ParseConfig's tokenizers, without its file and environment reads.
func scanPostgresDSN(cs string) (pgDSNScan, bool) {
	if strings.Contains(cs, "\x00") {
		return pgDSNScan{}, false
	}
	var settings map[string]string
	var ok bool
	if body, isURI := pgURIBody(cs); isURI {
		settings, ok = pgURISettings(body)
	} else {
		settings, ok = pgKeywordSettings(cs)
	}
	if !ok {
		return pgDSNScan{}, false
	}
	host, hostSet := settings["host"]
	return pgDSNScan{
		hostSet: hostSet,
		host:    host,
		claimsTLS: slices.Contains(pgTLSMandatorySSLModes, settings["sslmode"]) || settings["sslnegotiation"] == "direct" ||
			settings["sslrootcert"] != "" || settings["sslcert"] != "" || settings["sslkey"] != "",
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
func pgURISettings(p string) (map[string]string, bool) {
	settings := make(map[string]string)
	if i := strings.IndexAny(p, "@/"); i >= 0 && p[i] == '@' {
		p = p[i+1:]
	}
	hosts, p, ok := pgURIHosts(p)
	if !ok {
		return nil, false
	}
	if hosts != "" {
		host, ok := pgURIDecode(hosts)
		if !ok {
			return nil, false
		}
		settings["host"] = host
	}
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return settings, pgURIQuery(p[i+1:], settings)
	}
	return settings, true
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
func pgURIQuery(params string, settings map[string]string) bool {
	sslWasLast := false
	for params != "" {
		var pair string
		pair, params, _ = strings.Cut(params, "&")
		rawKey, rawValue, found := strings.Cut(pair, "=")
		if !found || strings.Contains(rawValue, "=") {
			return false
		}
		key, keyOK := pgURIDecode(rawKey)
		value, valueOK := pgURIDecode(rawValue)
		if !keyOK || !valueOK {
			return false
		}
		switch key {
		case "ssl":
			sslWasLast = true
		case "sslmode":
			sslWasLast = false
		}
		settings[key] = value
	}
	if sslWasLast && settings["ssl"] == "true" {
		settings["sslmode"] = sslModeRequire
	}
	return true
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
