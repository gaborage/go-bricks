package credurl

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testReason is the stand-in every row passes; the oracle package pins its own.
const testReason = "invalid URL"

// hint stands in for advice a driver appends after a url.Parse failure. Its quoted
// tokens prove that quote-bearing text outside the *url.Error survives.
const hint = `(hint: a reserved character such as "?" or "#" must be percent-encoded)`

// maskedRejection is the amqp rendering once a rejected password is masked and its
// quoting reason replaced; maskedCutAtPassword is that rendering when url.Parse cut
// the URL at a '#' inside the password.
const (
	maskedRejection     = `parse "amqp://user:xxxxx@{host}/vh": invalid URL ` + hint
	maskedCutAtPassword = `parse "amqp://user:xxxxx": invalid URL ` + hint
)

// hostSlot marks where a hostile-password template takes its host.
const hostSlot = "{host}"

// delHost carries a DEL byte, which url.Parse rejects and %q escapes.
const delHost = "zonk\x7fhost"

// parseVerdict pins what url.Parse makes of a row's raw URL, so a stdlib change that
// moves a shape across the accept/reject line fails the row's premise, not its want.
type parseVerdict int

const (
	parseUnpinned parseVerdict = iota
	parseAccepts
	parseRejects
)

type redactErrorCase struct {
	name    string
	raw     string
	err     func(raw string) error // nil: hintedErr
	parse   parseVerdict
	want    string   // the full Error() text; unused when same
	same    bool     // nothing to mask, so RedactError must return the original value
	secrets []string // substrings that must not survive in the result
}

// hintedErr is the default builder: url.Parse's failure wrapped with a trailing
// hint, or, for a URL url.Parse accepts, a dial failure that renders raw verbatim.
func hintedErr(raw string) error {
	if _, err := url.Parse(raw); err != nil {
		return fmt.Errorf("%w %s", err, hint)
	}
	return verbatimDialErr(raw)
}

// bareParseErr returns url.Parse's *url.Error unwrapped, the way go-ora does.
func bareParseErr(raw string) error {
	_, err := url.Parse(raw)
	return err
}

// verbatimDialErr renders raw verbatim whether or not url.Parse accepts it.
func verbatimDialErr(raw string) error {
	return fmt.Errorf("dial %s: connection refused", raw)
}

// TestNetURLErrorFormatUnchanged is a tripwire on the single stdlib assumption
// RedactError's maskURLError depends on: net/url renders url.Error as `%s %q: %s`
// over Op, URL, Err. maskURLError replaces urlErr.Error() with a rebuild of that
// exact format, so a format change makes the replacement silently stop matching and
// the password is exposed again — a redaction that fails open. If this test ever
// fails, revisit maskURLError before anything else.
func TestNetURLErrorFormatUnchanged(t *testing.T) {
	err := &url.Error{Op: "parse", URL: "x", Err: errors.New("boom")}

	assert.Equal(t, `parse "x": boom`, err.Error())
}

func TestRedactError(t *testing.T) {
	cases := slices.Concat(hostilePasswordCases(), digitPasswordCases(), atSignUserinfoCases(), scanCases(), seamCases())

	for i := range cases {
		tc := &cases[i]
		t.Run(tc.name, func(t *testing.T) {
			assertRedacted(t, tc)
		})
	}
}

func assertRedacted(t *testing.T, tc *redactErrorCase) {
	t.Helper()

	_, parseErr := url.Parse(tc.raw)
	switch tc.parse {
	case parseAccepts:
		require.NoError(t, parseErr, "premise: url.Parse accepts the raw URL")
	case parseRejects:
		require.Error(t, parseErr, "premise: url.Parse rejects the raw URL")
	case parseUnpinned:
		// A custom builder decides what the row renders.
	}

	build := tc.err
	if build == nil {
		build = hintedErr
	}
	original := build(tc.raw)
	require.Error(t, original, "premise: the row must start from an error")

	got := RedactError(original, tc.raw, testReason)

	if tc.same {
		assert.Same(t, original, got, "nothing to mask must return the original error")
	} else {
		assert.Equal(t, tc.want, got.Error())
	}
	require.ErrorIs(t, got, original, "the result must unwrap to the original error")
	var urlErr *url.Error
	if errors.As(original, &urlErr) {
		var gotURLErr *url.Error
		require.ErrorAs(t, got, &gotURLErr, "the *url.Error must stay reachable")
		assert.Same(t, urlErr, gotURLErr)
	}
	for _, secret := range tc.secrets {
		assert.NotContains(t, got.Error(), secret)
	}
}

// hostilePasswordCases runs each hostile password through the amqp shape on a named
// host, an IPv6 literal with a port and an IPv6 literal without one.
func hostilePasswordCases() []redactErrorCase {
	hosts := []hostVariant{
		{name: "named_host", addr: "broker:5672"},
		{name: "ipv6_with_port", addr: "[::1]:5672"},
		{name: "ipv6_without_port", addr: "[2001:db8::1]"},
	}
	maskedUserinfo := `parse "amqp://user:xxxxx@{host}/vh": net/url: invalid userinfo ` + hint
	maskedDial := "dial amqp://user:xxxxx@{host}/vh: connection refused"
	rejectedSecrets := []string{"s3cr", "etPW", "%et"}
	passwordSecrets := []string{"s3cr", "etPW"}

	templates := []redactErrorCase{
		// url.Parse rejects these with a reason that quotes part of the password — an
		// "invalid port" echo or an "invalid URL escape" — so the reason is replaced.
		{name: "password_with_question_mark", raw: "amqp://user:s3cr?etPW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: rejectedSecrets},
		{name: "password_with_slash", raw: "amqp://user:s3cr/etPW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: rejectedSecrets},
		{name: "password_with_invalid_escape", raw: "amqp://user:s3cr%etPW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: rejectedSecrets},
		// url.Parse cuts the fragment before it parses, so the URL it rejects — and so
		// the want — stops at "s3cr" and never reaches the host.
		{
			name:    "password_with_hash",
			raw:     "amqp://user:s3cr#etPW@{host}/vh",
			parse:   parseRejects,
			want:    maskedCutAtPassword,
			secrets: rejectedSecrets,
		},
		// "net/url: invalid userinfo" quotes nothing, so the reason survives.
		{
			name:    "password_with_double_quote",
			raw:     `amqp://user:s3cr"etPW@{host}/vh`,
			parse:   parseRejects,
			want:    maskedUserinfo,
			secrets: passwordSecrets,
		},
		{
			name:    "password_with_open_bracket",
			raw:     "amqp://user:s3cr[etPW@{host}/vh",
			parse:   parseRejects,
			want:    maskedUserinfo,
			secrets: passwordSecrets,
		},
		// url.Parse accepts these, so no *url.Error exists and only the verbatim
		// rendering of raw in the dial failure is masked.
		{name: "password_with_at", raw: "amqp://user:s3cr@etPW@{host}/vh", parse: parseAccepts, want: maskedDial, secrets: passwordSecrets},
		{name: "password_with_colon", raw: "amqp://user:s3cr:etPW@{host}/vh", parse: parseAccepts, want: maskedDial, secrets: passwordSecrets},
		{name: "empty_password", raw: "amqp://user:@{host}/vh", parse: parseAccepts, want: maskedDial},
		{
			name:    "percent_encoded_password",
			raw:     "amqp://user:s3cr%3FetPW@{host}/vh",
			parse:   parseAccepts,
			want:    maskedDial,
			secrets: []string{"s3cr", "etPW", "%3F"},
		},
		// A digits-only segment reads as a port, so url.Parse takes "user:1234" for the
		// host and never rejects the URL: the ambiguous shape is left alone on purpose.
		{name: "digit_password_reads_as_port", raw: "amqp://user:1234/etPW@{host}/vh", parse: parseAccepts, same: true},
		// url.Parse reads "et" as the host and the rest as the query, so "s3cr" is all
		// it reads as the password — and all that is masked.
		{
			name:    "password_with_at_then_question_mark",
			raw:     "amqp://user:s3cr@et?PW@{host}/vh",
			parse:   parseAccepts,
			want:    "dial amqp://user:xxxxx@et?PW@{host}/vh: connection refused",
			secrets: []string{"s3cr"},
		},
	}

	return withHosts(hosts, templates)
}

// hostVariant names the host a template's {host} slot takes.
type hostVariant struct{ name, addr string }

// portedHosts are the host variants that carry a port.
var portedHosts = []hostVariant{
	{name: "named_host", addr: "broker:5672"},
	{name: "ipv6_with_port", addr: "[::1]:5672"},
}

// withHosts expands every template once per host.
func withHosts(hosts []hostVariant, templates []redactErrorCase) []redactErrorCase {
	cases := make([]redactErrorCase, 0, len(hosts)*len(templates))
	for _, host := range hosts {
		for _, tc := range templates {
			tc.name += "_" + host.name
			tc.raw = strings.ReplaceAll(tc.raw, hostSlot, host.addr)
			tc.want = strings.ReplaceAll(tc.want, hostSlot, host.addr)
			cases = append(cases, tc)
		}
	}
	return cases
}

// digitPasswordCases pin a password that starts with digits before an unescaped '/',
// '?' or '#': the authority then reads as host:port ("app:2024"). url.Parse accepts
// that shape alone, but a second defect — a trailing newline from a secret file, a
// DEL byte, a leading space, a bad escape — makes it reject the URL and render it
// whole, so the '@' that follows must settle the segment as userinfo. Left out, since
// url.Parse accepts them and no *url.Error renders them: a newline or DEL after a '#'
// (the fragment is not checked for control characters) and "%zz" after a '?' (the
// query is not unescaped).
func digitPasswordCases() []redactErrorCase {
	const controlChar = ": net/url: invalid control character in URL " + hint
	const colonInFirstSegment = ": first path segment in URL cannot contain colon " + hint
	maskedNewline := "parse " + strconv.Quote("amqp://app:xxxxx@{host}/vh\n") + controlChar
	maskedDEL := "parse " + strconv.Quote("amqp://app:xxxxx@{host}/vh\x7f") + controlChar
	maskedSpace := `parse " amqp://app:xxxxx@{host}/vh"` + colonInFirstSegment
	maskedEscape := `parse "amqp://app:xxxxx@{host}/vh%zz": invalid URL ` + hint
	slashSecrets := []string{"2024", "Spring"}
	digitSecrets := []string{"2024"}

	templates := []redactErrorCase{
		{name: "slash_password_with_trailing_newline", raw: "amqp://app:2024/Spring@{host}/vh\n", parse: parseRejects, want: maskedNewline, secrets: slashSecrets},
		{name: "question_mark_password_with_trailing_newline", raw: "amqp://app:2024?x@{host}/vh\n", parse: parseRejects, want: maskedNewline, secrets: digitSecrets},
		{name: "slash_password_with_trailing_del", raw: "amqp://app:2024/Spring@{host}/vh\x7f", parse: parseRejects, want: maskedDEL, secrets: slashSecrets},
		{name: "question_mark_password_with_trailing_del", raw: "amqp://app:2024?x@{host}/vh\x7f", parse: parseRejects, want: maskedDEL, secrets: digitSecrets},
		{name: "slash_password_with_leading_space", raw: " amqp://app:2024/Spring@{host}/vh", parse: parseRejects, want: maskedSpace, secrets: slashSecrets},
		{name: "question_mark_password_with_leading_space", raw: " amqp://app:2024?x@{host}/vh", parse: parseRejects, want: maskedSpace, secrets: digitSecrets},
		// url.Parse reports the URL cut at the '#', so the '@' is only in raw beyond it.
		{
			name:    "hash_password_with_leading_space",
			raw:     " amqp://app:2024#x@{host}/vh",
			parse:   parseRejects,
			want:    `parse " amqp://app:xxxxx"` + colonInFirstSegment,
			secrets: digitSecrets,
		},
		{name: "slash_password_with_invalid_escape", raw: "amqp://app:2024/Spring@{host}/vh%zz", parse: parseRejects, want: maskedEscape, secrets: slashSecrets},
		// A fragment that fails to unescape makes url.Parse report the URL uncut.
		{name: "hash_password_with_invalid_escape", raw: "amqp://app:2024#x@{host}/vh%zz", parse: parseRejects, want: maskedEscape, secrets: digitSecrets},
	}

	return append(withHosts(portedHosts, templates),
		// With no '@' to follow, the host:port reading stands even once url.Parse rejects.
		redactErrorCase{name: "credential_free_url_with_trailing_newline", raw: "amqp://broker:5672/vh\n", parse: parseRejects, same: true},
		redactErrorCase{name: "credential_free_ipv6_url_with_trailing_newline", raw: "amqp://[::1]:5672/vh\n", parse: parseRejects, same: true},
		// What url.Parse cut counts only for an '@' in it.
		redactErrorCase{name: "credential_free_url_with_cut_fragment", raw: " amqp://broker:5672/vh#frag", parse: parseRejects, same: true},
		// Only an '@' after the scheme separator can follow the authority.
		redactErrorCase{name: "at_sign_before_scheme_is_not_userinfo", raw: "user@amqp://broker:5672/vh", parse: parseRejects, same: true},
		// The mirror image: no credentials, an '@' in the path, rejected for another
		// reason. It reads as userinfo and is over-masked, which is the safe direction.
		redactErrorCase{
			name:  "at_sign_in_path_of_rejected_credential_free_url",
			raw:   "oracle://host:1521/XE@weird\n",
			parse: parseRejects,
			want:  "parse " + strconv.Quote("oracle://host:xxxxx@weird\n") + controlChar,
		},
	)
}

// atSignUserinfoCases pin an '@' in the username, or in the password ahead of an
// unescaped '/', '?' or '#'. url.Parse splits the authority on that earlier '@', so
// the password lands where the port belongs; the userinfo still ends at the last '@'
// after the scheme separator, and runs to the end of what url.Parse reported when
// that '@' was cut away with the fragment.
func atSignUserinfoCases() []redactErrorCase {
	maskedUsername := `parse "amqp://svc@corp:xxxxx@{host}/vh": invalid URL ` + hint
	usernameSecrets := []string{"Se", "cretPW"}
	passwordSecrets := []string{"Sec", "re:t", "PW"}

	templates := []redactErrorCase{
		{name: "at_sign_username_password_with_slash", raw: "amqp://svc@corp:Se/cretPW@{host}/vh", parse: parseRejects, want: maskedUsername, secrets: usernameSecrets},
		{name: "at_sign_username_password_with_question_mark", raw: "amqp://svc@corp:Se?cretPW@{host}/vh", parse: parseRejects, want: maskedUsername, secrets: usernameSecrets},
		{
			name:    "at_sign_username_password_with_hash",
			raw:     "amqp://svc@corp:Se#cretPW@{host}/vh",
			parse:   parseRejects,
			want:    `parse "amqp://svc@corp:xxxxx": invalid URL ` + hint,
			secrets: usernameSecrets,
		},
		{name: "password_with_at_before_slash", raw: "amqp://user:Sec@re:t/PW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: passwordSecrets},
		{name: "password_with_at_before_question_mark", raw: "amqp://user:Sec@re:t?PW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: passwordSecrets},
		{
			name:    "password_with_at_before_hash",
			raw:     "amqp://user:Sec@re:t#PW@{host}/vh",
			parse:   parseRejects,
			want:    maskedCutAtPassword,
			secrets: passwordSecrets,
		},
	}

	return append(withHosts(portedHosts, templates),
		// The '@' url.Parse reported is not the last one: the last sits in the cut tail.
		redactErrorCase{
			name:    "password_with_at_and_colon_before_hash",
			raw:     "amqp://user:p@ss:x#y@broker/vh",
			parse:   parseRejects,
			want:    maskedCutAtPassword,
			secrets: []string{"p@ss", "ss:x"},
		},
		// An '@' in the path of a rejected credentialed URL ends the userinfo too, so
		// the host is masked with the password: the accepted, safe direction.
		redactErrorCase{
			name:    "at_sign_in_path_of_rejected_credentialed_url",
			raw:     "amqp://user:s3cr%zz@broker/vh@x",
			parse:   parseRejects,
			want:    `parse "amqp://user:xxxxx@x": invalid URL ` + hint,
			secrets: []string{"s3cr", "%zz"},
		},
	)
}

// scanCases drive the password scan and its host[:port] guard through RedactError
// with the oracle-shaped inputs the scan was first written against. An accepted URL
// lands verbatim in the default builder's dial failure, so its row observes the scan
// directly; a rejected one observes it through the rebuilt *url.Error.
func scanCases() []redactErrorCase {
	const rejectedDSN = `parse "oracle://appuser:xxxxx@host:1521/XE": invalid URL ` + hint
	const maskedDialDSN = "dial oracle://appuser:xxxxx@host:1521/XE: connection refused"
	invalidPortEcho := `":pa"`

	return []redactErrorCase{
		{name: "empty_url", raw: "", parse: parseAccepts, same: true},
		{name: "no_userinfo", raw: "oracle://host:1521/XE", parse: parseAccepts, same: true},
		{name: "user_only_no_colon", raw: "oracle://appuser@host:1521/XE", parse: parseAccepts, same: true},
		{name: "user_and_password", raw: "oracle://appuser:s3cr3t@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"s3cr3t"}},
		{name: "empty_password", raw: "oracle://appuser:@host:1521/XE", parse: parseAccepts, want: maskedDialDSN},
		{name: "password_with_at", raw: "oracle://appuser:pa@ss@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"pa@ss"}},
		{name: "password_with_colon", raw: "oracle://appuser:pa:ss@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"pa:ss"}},
		{
			name:    "password_with_slash",
			raw:     "oracle://appuser:pa/ss@host:1521/XE",
			parse:   parseRejects,
			want:    rejectedDSN,
			secrets: []string{"pa/ss", invalidPortEcho},
		},
		{
			name:    "password_with_question_mark",
			raw:     "oracle://appuser:pa?ss@host:1521/XE",
			parse:   parseRejects,
			want:    rejectedDSN,
			secrets: []string{"pa?ss", invalidPortEcho},
		},
		{
			name:    "at_sign_username_password_with_slash",
			raw:     "oracle://svc@corp:zqxvwSe/cretPW@host:1521/XE",
			parse:   parseRejects,
			want:    `parse "oracle://svc@corp:xxxxx@host:1521/XE": invalid URL ` + hint,
			secrets: []string{"zqxvwSe", "cretPW"},
		},
		// url.Parse cuts the fragment before it reports (the hostile rows pin that
		// path), so this row renders raw verbatim to observe the scan over the whole
		// URL: the '@' hides behind the '#', and the scan still finds it.
		{
			name:    "password_with_hash",
			raw:     "oracle://appuser:pa#ss@host:1521/XE",
			err:     verbatimDialErr,
			want:    maskedDialDSN,
			secrets: []string{"pa#ss"},
		},
		// What url.Parse is handed once it drops the fragment: no '@' survives, so the
		// userinfo is only recognizable by "appuser:pa" not being a valid host[:port].
		{
			name:    "fragment_truncated_userinfo",
			raw:     "oracle://appuser:pa",
			parse:   parseRejects,
			want:    `parse "oracle://appuser:xxxxx": invalid URL ` + hint,
			secrets: []string{invalidPortEcho},
		},
		// looksLikeHostPort rejects a non-numeric port, so this credential-free authority
		// reads as userinfo and everything past "host:" is masked — including the "/XE"
		// tail. Over-masking on purpose: the shape is one url.Parse rejects, so the URL
		// does reach an error message, and masking a URL that holds no password is the
		// safe direction. The accepted cost is that a plain port typo costs the operator
		// the invalid-port text and the service path.
		{
			name:  "credential_free_authority_with_non_numeric_port",
			raw:   "oracle://host:abc/XE",
			parse: parseRejects,
			want:  `parse "oracle://host:xxxxx": invalid URL ` + hint,
		},
		{name: "ipv6_host_with_port", raw: "oracle://[::1]:1521/XE", parse: parseAccepts, same: true},
		{name: "ipv6_host_without_port", raw: "oracle://[::1]/XE", parse: parseAccepts, same: true},
		// A zero-compressed literal ends in the ':' the port scan looks for, so it is the
		// only shape where skipping to the wrong side of the ']' changes the verdict.
		{name: "ipv6_zero_compressed_host_without_port", raw: "oracle://[::]/XE", parse: parseAccepts, same: true},
		// The scan tests each strings.Index result against -1, so index 0 must read as a
		// hit. One case per sentinel: scheme, authority terminator, '@', ':'.
		{
			name:    "scheme_separator_at_index_zero",
			raw:     "://appuser:s3cr3t@host:1521/XE",
			parse:   parseRejects,
			want:    `parse "://appuser:xxxxx@host:1521/XE": missing protocol scheme ` + hint,
			secrets: []string{"s3cr3t"},
		},
		{name: "authority_terminator_at_index_zero", raw: "oracle:///user:pass@host", parse: parseAccepts, same: true},
		{name: "at_sign_at_index_zero", raw: "oracle://@host:1521/XE", parse: parseAccepts, same: true},
		{
			name:    "empty_username_with_password",
			raw:     "oracle://:s3cr3t@host:1521/XE",
			parse:   parseAccepts,
			want:    "dial oracle://:xxxxx@host:1521/XE: connection refused",
			secrets: []string{"s3cr3t"},
		},
		{
			name:    "query_options_preserved",
			raw:     "oracle://appuser:s3cr3t@host:1521/XE?SID=XE",
			parse:   parseAccepts,
			want:    "dial oracle://appuser:xxxxx@host:1521/XE?SID=XE: connection refused",
			secrets: []string{"s3cr3t"},
		},
		// '#' terminates the authority too, so an '@' in the fragment is not userinfo.
		{name: "at_sign_only_in_fragment", raw: "oracle://host:1521#XE@weird", parse: parseAccepts, same: true},
		{name: "scheme_without_authority", raw: "oracle://", parse: parseAccepts, same: true},
		// A reason that quotes nothing survives the rebuild.
		{
			name:    "unparseable_host_still_masked",
			raw:     "oracle://appuser:s3cr3t@" + delHost + ":1521/XE",
			parse:   parseRejects,
			want:    "parse " + strconv.Quote("oracle://appuser:xxxxx@"+delHost+":1521/XE") + ": net/url: invalid control character in URL " + hint,
			secrets: []string{"s3cr3t"},
		},
		// Non-URL strings pass through: the leak is url.Parse's %q rendering of what it
		// was handed, which only fires for the URL form.
		{name: "ez_connect_not_a_url", raw: "appuser/s3cr3t@host:1521/XE", parse: parseAccepts, same: true},
		{
			name:  "tns_descriptor_not_a_url",
			raw:   "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=host)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=XE)))",
			parse: parseAccepts,
			same:  true,
		},
		// The looksLikeHostPort guard is the whole reason these two are left unmasked:
		// url.Parse ACCEPTS both, so neither is ever rendered into a parse error. They
		// are the same shape read two ways — credentials in the first, host:port plus an
		// '@' in the path in the second — so masking one necessarily mangles the other,
		// for no security gain.
		{name: "digit_password_segment_reads_as_a_real_port", raw: "oracle://user:1234/secret@db", parse: parseAccepts, same: true},
		{name: "host_port_with_at_sign_in_path", raw: "oracle://host:1521/XE@weird", parse: parseAccepts, same: true},
		// looksLikeHostPort verdicts. Each accepted authority carries a ':' the scan would
		// mask on a false verdict — the path's ":1" for the bare host.
		{name: "bare_host", raw: "oracle://host/XE:1", parse: parseAccepts, same: true},
		{name: "host_with_numeric_port", raw: "oracle://host:1521", parse: parseAccepts, same: true},
		{name: "userinfo_with_non_numeric_tail", raw: "oracle://user:pa", parse: parseRejects, want: `parse "oracle://user:xxxxx": invalid URL ` + hint},
		{name: "bracketed_ipv6_without_port", raw: "oracle://[::1]", parse: parseAccepts, same: true},
		// Skipping to the wrong side of the ']' leaves a ':' behind and turns the
		// trailing "]" into the port.
		{name: "zero_compressed_ipv6_without_port", raw: "oracle://[::]", parse: parseAccepts, same: true},
		{name: "bracketed_ipv6_with_port", raw: "oracle://[2001:db8::1]:1521", parse: parseAccepts, same: true},
		// Only the segment after the last ':' is the port.
		{name: "unbracketed_multi_colon_host_with_port", raw: "oracle://fe80::1:1521/XE", parse: parseAccepts, same: true},
		// LastIndex returns -1 for an unterminated literal, so the +1 slice is a no-op
		// and the whole segment is examined: "db8" is not a port, so the segment reads as
		// userinfo and over-masks. The reason quotes nothing, so it survives.
		{
			name:  "unterminated_ipv6_literal",
			raw:   "oracle://[2001:db8",
			parse: parseRejects,
			want:  `parse "oracle://[2001:xxxxx": missing ']' in host ` + hint,
		},
	}
}

// seamCases pin the rendering seams around the scan: raw rendered outside any
// *url.Error, a *url.Error built by hand, and errors with nothing to mask.
func seamCases() []redactErrorCase {
	const password = "zqxvwgirdlespan"
	// The DEL byte makes %q escape the URL, so the quoted rendering is not a substring
	// of the message and only the second replacement can reach it.
	leaky := "oracle://appuser:" + password + "@" + delHost + ":1521/XE"
	masked := "oracle://appuser:xxxxx@" + delHost + ":1521/XE"
	bothRenderings := func(raw string) error {
		return errors.New("dial " + raw + ": parse " + strconv.Quote(raw) + ": bad")
	}

	return []redactErrorCase{
		{
			name: "url_without_password_leaves_the_message_alone",
			raw:  "oracle://host:1521/XE",
			err:  bothRenderings,
			same: true,
		},
		{
			name:    "verbatim_and_quoted_renderings_both_masked",
			raw:     leaky,
			err:     bothRenderings,
			want:    "dial " + masked + ": parse " + strconv.Quote(masked) + ": bad",
			secrets: []string{password},
		},
		// url.Error is a plain struct, so a driver can hand one back with no wrapped
		// reason at all; the stand-in takes its place.
		{
			name:    "url_error_without_reason",
			raw:     "amqp://user:zqxvwbarrowclasp@broker:5672/vh",
			err:     func(raw string) error { return &url.Error{Op: "parse", URL: raw} },
			want:    `parse "amqp://user:xxxxx@broker:5672/vh": invalid URL`,
			secrets: []string{"zqxvwbarrowclasp"},
		},
		{
			name:    "reason_without_quote_survives",
			raw:     "amqp://user:zqxvwlanternquill@" + delHost + ":5672/vh",
			err:     bareParseErr,
			parse:   parseRejects,
			want:    "parse " + strconv.Quote("amqp://user:xxxxx@"+delHost+":5672/vh") + ": net/url: invalid control character in URL",
			secrets: []string{"zqxvwlanternquill"},
		},
		// The *url.Error can name a URL raw does not start with, such as a dialer's own;
		// it is still masked, and nothing is read past raw's end.
		{
			name:    "url_error_naming_another_url",
			raw:     "amqp://localhost",
			err:     func(string) error { return bareParseErr("amqp://app:2024/Spring@broker:5672/vh\n") },
			want:    "parse " + strconv.Quote("amqp://app:xxxxx@broker:5672/vh\n") + ": net/url: invalid control character in URL",
			secrets: []string{"2024", "Spring"},
		},
		{
			name: "url_error_without_password",
			raw:  "amqp://broker:5672/vh",
			err: func(string) error {
				return &url.Error{Op: "parse", URL: "amqp://broker:5672/vh", Err: errors.New("boom")}
			},
			same: true,
		},
		// No password to mask, so even a reason that quotes the URL is kept for the operator.
		{name: "credential_free_url_keeps_quoting_reason", raw: "amqp://broker:5672/vh%zz", parse: parseRejects, same: true},
		{
			name: "message_omits_raw",
			raw:  "amqp://user:s3cr3t@broker:5672/vh",
			err:  func(string) error { return errors.New("context deadline exceeded") },
			same: true,
		},
		{
			name: "raw_without_password",
			raw:  "amqp://broker:5672/vh",
			err:  func(string) error { return errors.New("boom: amqp://broker:5672/vh") },
			same: true,
		},
	}
}
