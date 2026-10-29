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

// hint is the advice amqp091 appends after a url.Parse failure, verbatim. Its quoted
// tokens prove that quote-bearing text outside the *url.Error survives.
const hint = `(if the username or password contains a reserved character such as "?", "#", "@", ":", or "/", ` +
	`it must be percent-encoded per RFC 3986 §3.2.1 -- see ParseURI's doc comment and net/url.UserPassword)`

// maskedRejection is the amqp rendering once a rejected password is masked and its
// quoting reason replaced; maskedTailRejection is that rendering when a '?' before the
// last '@' masks the whole tail.
const (
	maskedRejection     = `parse "amqp://user:xxxxx@{host}/vh": invalid URL ` + hint
	maskedTailRejection = `parse "amqp://user:xxxxx@xxxxx": invalid URL ` + hint
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

// fixedErr builds an error whose text ignores raw.
func fixedErr(text string) func(string) error {
	return func(string) error { return errors.New(text) }
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

func TestRedactRendersOnePosture(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		parse   parseVerdict
		want    string
		secrets []string // substrings that must not survive in the result
	}{
		{name: "password_without_query", raw: "amqp://user:zq9x@broker:5672/vh", parse: parseAccepts, want: "amqp://user:xxxxx@broker:5672/vh"},
		{name: "password_with_query", raw: "amqp://user:zq9x@broker:5672/vh?heartbeat=30&token=zq9x", parse: parseAccepts, want: "amqp://user:xxxxx@broker:5672/vh?xxxxx"},
		{name: "query_without_userinfo", raw: "amqp://broker:5672/vh?token=zq9x", parse: parseAccepts, want: "amqp://broker:5672/vh?xxxxx"},
		{name: "empty_query_emits_nothing", raw: "amqp://user:zq9x@broker:5672/vh?", parse: parseAccepts, want: "amqp://user:xxxxx@broker:5672/vh"},
		{name: "fragment_dropped", raw: "amqp://user:zq9x@broker:5672/vh?token=zq9x#zq9x", parse: parseAccepts, want: "amqp://user:xxxxx@broker:5672/vh?xxxxx"},
		{name: "fragment_dropped_without_query", raw: "amqp://broker:5672/vh#zq9x", parse: parseAccepts, want: "amqp://broker:5672/vh"},
		{name: "no_userinfo", raw: "amqp://broker:5672/vh", parse: parseAccepts, want: "amqp://broker:5672/vh"},
		// A lone userinfo can be an API key passed as the username, and an empty
		// password leaves the username alone in it, so both are masked whole.
		{name: "lone_userinfo", raw: "amqp://zq9x@broker:5672/vh", parse: parseAccepts, want: "amqp://xxxxx@broker:5672/vh"},
		{name: "empty_password", raw: "amqp://zq9x:@broker:5672/vh", parse: parseAccepts, want: "amqp://xxxxx@broker:5672/vh"},
		// A later '@' stretches the userinfo past the host, so the key sits in a username holding an '@'.
		{name: "lone_userinfo_before_at_in_path", raw: "oracle://APIKEY@host:1521/XE@vhost", parse: parseAccepts, want: "oracle://xxxxx@vhost", secrets: []string{"APIKEY"}},
		{name: "empty_password_before_at_in_path", raw: "oracle://APIKEY:@host:1521/XE@vhost", parse: parseAccepts, want: "oracle://xxxxx@vhost", secrets: []string{"APIKEY"}},
		{name: "lone_userinfo_before_at_in_query", raw: "oracle://APIKEY@host:1521/XE?a=b@c", parse: parseAccepts, want: "oracle://xxxxx@xxxxx", secrets: []string{"APIKEY"}},
		// A scanned username crossing out of the authority is path, query or fragment text.
		{name: "path_before_first_colon", raw: "oracle://host/XE/SECRET:abc@def", parse: parseAccepts, want: "oracle://xxxxx@def", secrets: []string{"SECRET"}},
		{name: "query_before_first_colon", raw: "oracle://host?wallet=SECRET:abc@def", parse: parseAccepts, want: "oracle://xxxxx@xxxxx", secrets: []string{"SECRET"}},
		{name: "fragment_before_first_colon", raw: "oracle://host#SECRET:abc@def", parse: parseAccepts, want: "oracle://xxxxx@def", secrets: []string{"SECRET"}},
		// Username and path are kept as written, never decoded.
		{name: "escaped_username_and_vhost", raw: "amqp://us%40er:zq9x@broker:5672/%2Fproduction", parse: parseAccepts, want: "amqp://us%40er:xxxxx@broker:5672/%2Fproduction"},
		{name: "hostile_password_characters", raw: "amqp://user:zq9x?#/@:PW@broker:5672/vh", parse: parseRejects, want: "amqp://user:xxxxx@xxxxx"},
		{name: "password_with_invalid_escape", raw: "amqp://user:s3cr%etPW@broker:5672/vh", parse: parseRejects, want: "amqp://user:xxxxx@broker:5672/vh"},
		{name: "rejected_without_at", raw: "amqp://user:pass/broker", parse: parseRejects, want: "amqp://user:xxxxx/broker"},
		{name: "rejected_without_at_with_query", raw: "amqp://user:zq9x?token=zq9x", parse: parseRejects, want: "amqp://user:xxxxx?xxxxx"},
		{name: "rejected_ipv6_without_at", raw: "amqp://[::1]:5672/vh\n", parse: parseRejects, want: "amqp://[::1]:xxxxx/vh\n"},
		// url.Parse accepts these but splits the userinfo at the wrong '@'.
		{name: "misparse_digit_password", raw: "amqp://user:1234/Spring@broker/vh", parse: parseAccepts, want: "amqp://user:xxxxx@broker/vh"},
		{name: "misparse_at_then_question_mark", raw: "amqp://user:s3cr@zq9x?PW@broker:5672/vh", parse: parseAccepts, want: "amqp://user:xxxxx@xxxxx"},
		{name: "stream_misparse_digit_password", raw: "rabbitmq-stream://app:2024/Sp/ring@broker:5552", parse: parseAccepts, want: "rabbitmq-stream://app:xxxxx@broker:5552"},
		// The accepted cost of reading to the last '@': one in a vhost over-masks everything
		// before it, and one after a '?' masks the whole tail, which may be query text.
		{name: "at_in_path_over_masks", raw: "amqp://u:p@h/my@vhost", parse: parseAccepts, want: "amqp://u:xxxxx@vhost"},
		{name: "at_in_query_over_masks", raw: "amqp://user:zq9x@broker:5672/vh?connection_name=app@QZXJ", parse: parseAccepts, want: "amqp://user:xxxxx@xxxxx", secrets: []string{"zq9x", "connection_name", "QZXJ"}},
		{name: "oracle_wallet_password_with_at", raw: "oracle://scott:tiger@db:1521/svc?WALLET%20PASSWORD=ab@QZXJKV", parse: parseAccepts, want: "oracle://scott:xxxxx@xxxxx", secrets: []string{"QZXJKV", "tiger"}},
		{name: "ipv6_with_port", raw: "amqp://user:zq9x@[::1]:5672/vh", parse: parseAccepts, want: "amqp://user:xxxxx@[::1]:5672/vh"},
		{name: "ipv6_without_port", raw: "amqp://user:zq9x@[2001:db8::1]/vh", parse: parseAccepts, want: "amqp://user:xxxxx@[2001:db8::1]/vh"},
		{name: "amqps_scheme", raw: "amqps://admin:zq9x@rabbitmq.prod:5671/production", parse: parseAccepts, want: "amqps://admin:xxxxx@rabbitmq.prod:5671/production"},
		{name: "stream_scheme", raw: "rabbitmq-stream://svc:zq9x@broker:5552/%2f", parse: parseAccepts, want: "rabbitmq-stream://svc:xxxxx@broker:5552/%2f"},
		{name: "no_scheme_separator", raw: "user:zq9x@host", want: redactionPlaceholder},
		{name: "host_port_without_scheme", raw: "localhost:5672", want: redactionPlaceholder},
		// A "://" after a character no scheme holds is not the scheme separator.
		{name: "scheme_separator_only_in_query", raw: "user:zq9x@host/x?next=amqp://h", want: redactionPlaceholder},
		{name: "empty", raw: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireParseVerdict(t, tc.raw, tc.parse)
			assert.Equal(t, tc.want, Redact(tc.raw))
			for _, secret := range tc.secrets {
				assert.NotContains(t, Redact(tc.raw), secret)
			}
		})
	}
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

	requireParseVerdict(t, tc.raw, tc.parse)
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

func requireParseVerdict(t *testing.T, raw string, verdict parseVerdict) {
	t.Helper()
	_, parseErr := url.Parse(raw)
	switch verdict {
	case parseAccepts:
		require.NoError(t, parseErr, "premise: url.Parse accepts the raw URL")
	case parseRejects:
		require.Error(t, parseErr, "premise: url.Parse rejects the raw URL")
	case parseUnpinned:
		// The row's builder or input decides what it renders.
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
		{name: "password_with_question_mark", raw: "amqp://user:s3cr?etPW@{host}/vh", parse: parseRejects, want: maskedTailRejection, secrets: rejectedSecrets},
		{name: "password_with_slash", raw: "amqp://user:s3cr/etPW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: rejectedSecrets},
		{name: "password_with_invalid_escape", raw: "amqp://user:s3cr%etPW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: rejectedSecrets},
		// url.Parse cuts the fragment before it parses, so the URL it rejects stops at
		// "s3cr"; the rebuild renders raw, whose last '@' is past the cut.
		{
			name:    "password_with_hash",
			raw:     "amqp://user:s3cr#etPW@{host}/vh",
			parse:   parseRejects,
			want:    maskedRejection,
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
		{name: "empty_password", raw: "amqp://user:@{host}/vh", parse: parseAccepts, want: "dial amqp://xxxxx@{host}/vh: connection refused", secrets: []string{"user"}},
		{
			name:    "percent_encoded_password",
			raw:     "amqp://user:s3cr%3FetPW@{host}/vh",
			parse:   parseAccepts,
			want:    maskedDial,
			secrets: []string{"s3cr", "etPW", "%3F"},
		},
		// A digits-only segment reads as a port, so url.Parse takes "user:1234" for the
		// host; the '@' after that authority sends the scan to the last '@'.
		{name: "digit_password_reads_as_port", raw: "amqp://user:1234/etPW@{host}/vh", parse: parseAccepts, want: maskedDial, secrets: []string{"1234", "etPW"}},
		// url.Parse reads "et" as the host and the rest as the query; the scan still
		// reads to the last '@', and the '?' before it masks the whole tail.
		{
			name:    "password_with_at_then_question_mark",
			raw:     "amqp://user:s3cr@et?PW@{host}/vh",
			parse:   parseAccepts,
			want:    "dial amqp://user:xxxxx@xxxxx: connection refused",
			secrets: []string{"s3cr", "et?PW", "PW"},
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
	maskedTail := `parse "amqp://app:xxxxx@xxxxx"` + controlChar
	slashSecrets := []string{"2024", "Spring"}
	digitSecrets := []string{"2024"}

	templates := []redactErrorCase{
		{name: "slash_password_with_trailing_newline", raw: "amqp://app:2024/Spring@{host}/vh\n", parse: parseRejects, want: maskedNewline, secrets: slashSecrets},
		{name: "question_mark_password_with_trailing_newline", raw: "amqp://app:2024?x@{host}/vh\n", parse: parseRejects, want: maskedTail, secrets: digitSecrets},
		{name: "slash_password_with_trailing_del", raw: "amqp://app:2024/Spring@{host}/vh\x7f", parse: parseRejects, want: maskedDEL, secrets: slashSecrets},
		{name: "question_mark_password_with_trailing_del", raw: "amqp://app:2024?x@{host}/vh\x7f", parse: parseRejects, want: maskedTail, secrets: digitSecrets},
		{name: "slash_password_with_leading_space", raw: " amqp://app:2024/Spring@{host}/vh", parse: parseRejects, want: maskedSpace, secrets: slashSecrets},
		{name: "question_mark_password_with_leading_space", raw: " amqp://app:2024?x@{host}/vh", parse: parseRejects, want: `parse " amqp://app:xxxxx@xxxxx"` + colonInFirstSegment, secrets: digitSecrets},
		// url.Parse reports the URL cut at the '#'; the '@' past it is in raw.
		{
			name:    "hash_password_with_leading_space",
			raw:     " amqp://app:2024#x@{host}/vh",
			parse:   parseRejects,
			want:    maskedSpace,
			secrets: []string{"2024", "#x"},
		},
		{name: "slash_password_with_invalid_escape", raw: "amqp://app:2024/Spring@{host}/vh%zz", parse: parseRejects, want: maskedEscape, secrets: slashSecrets},
		// A fragment that fails to unescape makes url.Parse report the URL uncut.
		{name: "hash_password_with_invalid_escape", raw: "amqp://app:2024#x@{host}/vh%zz", parse: parseRejects, want: maskedEscape, secrets: digitSecrets},
	}

	return append(withHosts(portedHosts, templates),
		// With no '@' at all, a rejected URL is masked from its first ':' to the end of
		// its authority: a credential-free port is over-masked.
		redactErrorCase{name: "credential_free_url_with_trailing_newline", raw: "amqp://broker:5672/vh\n", parse: parseRejects, want: `parse "amqp://broker:xxxxx/vh\n"` + controlChar, secrets: []string{"5672"}},
		redactErrorCase{name: "credential_free_ipv6_url_with_trailing_newline", raw: "amqp://[::1]:5672/vh\n", parse: parseRejects, want: `parse "amqp://[::1]:xxxxx/vh\n"` + controlChar, secrets: []string{"5672"}},
		redactErrorCase{name: "credential_free_ipv6_url_without_port", raw: "amqp://[::]/vh\n", parse: parseRejects, same: true},
		redactErrorCase{name: "credential_free_url_with_cut_fragment", raw: " amqp://broker:5672/vh#frag", parse: parseRejects, want: `parse " amqp://broker:xxxxx/vh"` + colonInFirstSegment, secrets: []string{"5672"}},
		// An '@' before the "://" leaves no scheme ahead of it, so the input is masked whole.
		redactErrorCase{name: "at_sign_before_scheme_masks_whole", raw: "user@amqp://broker:5672/vh", parse: parseRejects, want: `parse "xxxxx"` + colonInFirstSegment, secrets: []string{"user@"}},
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
// after the scheme separator, even one url.Parse cut away with the fragment, and an '@'
// username is masked whole with its userinfo.
func atSignUserinfoCases() []redactErrorCase {
	maskedUsername := `parse "amqp://xxxxx@{host}/vh": invalid URL ` + hint
	usernameSecrets := []string{"Se", "cretPW", "svc@corp"}
	passwordSecrets := []string{"Sec", "re:t", "PW"}

	templates := []redactErrorCase{
		{name: "at_sign_username_password_with_slash", raw: "amqp://svc@corp:Se/cretPW@{host}/vh", parse: parseRejects, want: maskedUsername, secrets: usernameSecrets},
		{name: "at_sign_username_password_with_question_mark", raw: "amqp://svc@corp:Se?cretPW@{host}/vh", parse: parseRejects, want: `parse "amqp://xxxxx@xxxxx": invalid URL ` + hint, secrets: usernameSecrets},
		{
			name:    "at_sign_username_password_with_hash",
			raw:     "amqp://svc@corp:Se#cretPW@{host}/vh",
			parse:   parseRejects,
			want:    maskedUsername,
			secrets: usernameSecrets,
		},
		{name: "password_with_at_before_slash", raw: "amqp://user:Sec@re:t/PW@{host}/vh", parse: parseRejects, want: maskedRejection, secrets: passwordSecrets},
		{name: "password_with_at_before_question_mark", raw: "amqp://user:Sec@re:t?PW@{host}/vh", parse: parseRejects, want: maskedTailRejection, secrets: passwordSecrets},
		{
			name:    "password_with_at_before_hash",
			raw:     "amqp://user:Sec@re:t#PW@{host}/vh",
			parse:   parseRejects,
			want:    maskedRejection,
			secrets: passwordSecrets,
		},
	}

	return append(withHosts(portedHosts, templates),
		// The '@' url.Parse reported is not the last one: the last sits in the cut tail.
		redactErrorCase{
			name:    "password_with_at_and_colon_before_hash",
			raw:     "amqp://user:p@ss:x#y@broker/vh",
			parse:   parseRejects,
			want:    `parse "amqp://user:xxxxx@broker/vh": invalid URL ` + hint,
			secrets: []string{"p@ss", "ss:x", "#y"},
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

// scanCases drive the password scan through RedactError
// with the oracle-shaped inputs the scan was first written against. An accepted URL
// lands verbatim in the default builder's dial failure, so its row observes the scan
// directly; a rejected one observes it through the rebuilt *url.Error.
func scanCases() []redactErrorCase {
	const rejectedDSN = `parse "oracle://appuser:xxxxx@host:1521/XE": invalid URL ` + hint
	const maskedDialDSN = "dial oracle://appuser:xxxxx@host:1521/XE: connection refused"
	const maskedLoneDSN = "dial oracle://xxxxx@host:1521/XE: connection refused"
	const maskedWhole = "dial xxxxx: connection refused"
	invalidPortEcho := `":pa"`

	return []redactErrorCase{
		{name: "empty_url", raw: "", parse: parseAccepts, same: true},
		{name: "no_userinfo", raw: "oracle://host:1521/XE", parse: parseAccepts, same: true},
		{name: "user_only_no_colon", raw: "oracle://appuser@host:1521/XE", parse: parseAccepts, want: maskedLoneDSN, secrets: []string{"appuser"}},
		{name: "user_and_password", raw: "oracle://appuser:s3cr3t@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"s3cr3t"}},
		{name: "empty_password", raw: "oracle://appuser:@host:1521/XE", parse: parseAccepts, want: maskedLoneDSN, secrets: []string{"appuser"}},
		{name: "password_with_at", raw: "oracle://appuser:pa@ss@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"pa@ss"}},
		{name: "password_with_colon", raw: "oracle://appuser:pa:ss@host:1521/XE", parse: parseAccepts, want: maskedDialDSN, secrets: []string{"pa:ss"}},
		{name: "path_before_first_colon", raw: "oracle://host/XE/SECRET:abc@def", parse: parseAccepts, want: "dial oracle://xxxxx@def: connection refused", secrets: []string{"SECRET"}},
		{name: "query_before_first_colon", raw: "oracle://host?wallet=SECRET:abc@def", parse: parseAccepts, want: "dial oracle://xxxxx@xxxxx: connection refused", secrets: []string{"SECRET"}},
		{name: "fragment_before_first_colon", raw: "oracle://host#SECRET:abc@def", parse: parseAccepts, want: "dial oracle://xxxxx@def: connection refused", secrets: []string{"SECRET"}},
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
			want:    `parse "oracle://appuser:xxxxx@xxxxx": invalid URL ` + hint,
			secrets: []string{"pa?ss", invalidPortEcho},
		},
		{
			name:    "at_sign_username_password_with_slash",
			raw:     "oracle://svc@corp:zqxvwSe/cretPW@host:1521/XE",
			parse:   parseRejects,
			want:    `parse "oracle://xxxxx@host:1521/XE": invalid URL ` + hint,
			secrets: []string{"zqxvwSe", "cretPW", "svc@corp"},
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
		// A verbatim echo of a rejected raw whose digit-led password reads as a port.
		{
			name:    "rejected_raw_echoed_verbatim",
			raw:     "amqp://user:2024/Spring@broker:5672/vh\n",
			parse:   parseRejects,
			err:     verbatimDialErr,
			want:    "dial amqp://user:xxxxx@broker:5672/vh\n: connection refused",
			secrets: []string{"2024", "Spring"},
		},
		// What url.Parse is handed once it drops the fragment: no '@' survives, so only
		// the rejection marks "appuser:pa" as more than a host.
		{
			name:    "fragment_truncated_userinfo",
			raw:     "oracle://appuser:pa",
			parse:   parseRejects,
			want:    `parse "oracle://appuser:xxxxx": invalid URL ` + hint,
			secrets: []string{invalidPortEcho},
		},
		// Over-masking on purpose: a port typo costs the operator the invalid-port text.
		{
			name:    "credential_free_authority_with_non_numeric_port",
			raw:     "oracle://host:abc/XE",
			parse:   parseRejects,
			want:    `parse "oracle://host:xxxxx/XE": invalid URL ` + hint,
			secrets: []string{"abc"},
		},
		{name: "ipv6_host_with_port", raw: "oracle://[::1]:1521/XE", parse: parseAccepts, same: true},
		{name: "ipv6_host_without_port", raw: "oracle://[::1]/XE", parse: parseAccepts, same: true},
		{name: "ipv6_zero_compressed_host_without_port", raw: "oracle://[::]/XE", parse: parseAccepts, same: true},
		// Index 0 must read as a hit for each sentinel: the scheme separator, a rejected
		// URL's authority terminator, the '@' and the ':'. An accepted URL whose path
		// starts at index 0 still has its userinfo read to the last '@'.
		{
			name:    "scheme_separator_at_index_zero",
			raw:     "://appuser:s3cr3t@host:1521/XE",
			parse:   parseRejects,
			want:    `parse "://appuser:xxxxx@host:1521/XE": missing protocol scheme ` + hint,
			secrets: []string{"s3cr3t"},
		},
		{name: "authority_terminator_at_index_zero", raw: "oracle:///user:pass@host", parse: parseAccepts, want: "dial oracle://xxxxx@host: connection refused", secrets: []string{"pass"}},
		{name: "rejected_authority_terminator_at_index_zero", raw: "oracle:///XE:1\n", parse: parseRejects, same: true},
		{name: "at_sign_at_index_zero", raw: "oracle://@host:1521/XE", parse: parseAccepts, want: maskedLoneDSN, secrets: []string{"//@"}},
		{
			name:    "empty_username_with_password",
			raw:     "oracle://:s3cr3t@host:1521/XE",
			parse:   parseAccepts,
			want:    "dial oracle://:xxxxx@host:1521/XE: connection refused",
			secrets: []string{"s3cr3t"},
		},
		{
			name:    "query_masked_whole",
			raw:     "oracle://appuser:s3cr3t@host:1521/XE?SID=XE",
			parse:   parseAccepts,
			want:    "dial oracle://appuser:xxxxx@host:1521/XE?xxxxx: connection refused",
			secrets: []string{"s3cr3t", "SID", "=XE"},
		},
		{name: "scheme_without_authority", raw: "oracle://", parse: parseAccepts, same: true},
		// A reason that quotes nothing survives the rebuild.
		{
			name:    "unparseable_host_still_masked",
			raw:     "oracle://appuser:s3cr3t@" + delHost + ":1521/XE",
			parse:   parseRejects,
			want:    "parse " + strconv.Quote("oracle://appuser:xxxxx@"+delHost+":1521/XE") + ": net/url: invalid control character in URL " + hint,
			secrets: []string{"s3cr3t"},
		},
		{name: "ez_connect_not_a_url", raw: "appuser/s3cr3t@host:1521/XE", parse: parseAccepts, want: maskedWhole, secrets: []string{"s3cr3t"}},
		{
			name:  "tns_descriptor_not_a_url",
			raw:   "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=host)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=XE)))",
			parse: parseAccepts,
			want:  maskedWhole,
		},
		// The same shape read two ways — credentials in the first, host:port plus an '@'
		// in the path or fragment in the others: url.Parse accepts all three, and the '@'
		// after its authority sends the scan to it, so the others are over-masked.
		{name: "digit_password_segment_reads_as_a_real_port", raw: "oracle://user:1234/secret@db", parse: parseAccepts, want: "dial oracle://user:xxxxx@db: connection refused", secrets: []string{"1234", "secret"}},
		{name: "host_port_with_at_sign_in_path", raw: "oracle://host:1521/XE@weird", parse: parseAccepts, want: "dial oracle://host:xxxxx@weird: connection refused", secrets: []string{"1521"}},
		{name: "at_sign_only_in_fragment", raw: "oracle://host:1521#XE@weird", parse: parseAccepts, want: "dial oracle://host:xxxxx@weird: connection refused", secrets: []string{"1521", "#XE"}},
		// An accepted URL with no '@' keeps its authority as url.Parse reads it.
		{name: "bare_host", raw: "oracle://host/XE:1", parse: parseAccepts, same: true},
		{name: "host_with_numeric_port", raw: "oracle://host:1521", parse: parseAccepts, same: true},
		{name: "userinfo_with_non_numeric_tail", raw: "oracle://user:pa", parse: parseRejects, want: `parse "oracle://user:xxxxx": invalid URL ` + hint},
		{name: "bracketed_ipv6_without_port", raw: "oracle://[::1]", parse: parseAccepts, same: true},
		{name: "zero_compressed_ipv6_without_port", raw: "oracle://[::]", parse: parseAccepts, same: true},
		{name: "bracketed_ipv6_with_port", raw: "oracle://[2001:db8::1]:1521", parse: parseAccepts, same: true},
		{name: "unbracketed_multi_colon_host_with_port", raw: "oracle://fe80::1:1521/XE", parse: parseAccepts, same: true},
		// An unterminated literal has no ']' to skip to, so the first ':' inside it is
		// masked from. The reason quotes nothing, so it survives.
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
		// A vendor reason can quote raw text outside any *url.Error, such as amqp091's
		// port and query checks; quoted text Redact keeps, or that is not raw's, stays.
		{
			name:    "vendor_quote_of_raw_text_masked",
			raw:     "amqp://user:12345678901/zq9x@broker/vh",
			err:     fixedErr(`strconv.ParseInt: parsing "12345678901": value out of range`),
			want:    `strconv.ParseInt: parsing "xxxxx": value out of range`,
			secrets: []string{"12345678901"},
		},
		{
			name:    "stray_quotes_do_not_hide_a_quoted_secret",
			raw:     "amqp://user:12345678901/zq9x@broker/vh",
			err:     fixedErr(`say "hi parsing "12345678901": out of range "`),
			want:    `say "hi parsing "xxxxx": out of range "`,
			secrets: []string{"12345678901"},
		},
		// Pieces not aligned in raw, which only the quoted pass masks, behind a stray quote and back to back.
		{
			name:    "unaligned_quoted_secrets_masked",
			raw:     "amqp://user:s3cr3tPW@broker/vh",
			parse:   parseAccepts,
			err:     fixedErr(`say "hi parsing "cr3t""s3cr": out of range "`),
			want:    `say "hi parsing "xxxxx""xxxxx": out of range "`,
			secrets: []string{"cr3t", "s3cr"},
		},
		// A one-byte quote reveals at most that byte, so the vendor's hint
		// survives a password holding the characters it quotes.
		{name: "reserved_character_hint_survives", raw: "amqp://user:s3cr?et#PW@broker/vh", parse: parseRejects, want: `parse "amqp://user:xxxxx@xxxxx": invalid URL ` + hint, secrets: []string{"s3cr", "et#PW", "PW"}},
		{name: "broker_reason_quote_survives", raw: "amqp://user:zq9x@broker/vh", err: fixedErr(`Reason: "ACCESS_REFUSED - Login was refused"`), same: true, secrets: []string{"zq9x"}},
		{name: "quoted_kept_path_survives", raw: "amqp://user:zq9x@broker/vh", err: fixedErr(`no access to vhost "/vh"`), same: true, secrets: []string{"zq9x"}},
		// Vendor text can echo raw unquoted, as a misparsed vhost or a dial host.
		{
			name:    "unquoted_vhost_echo_masked",
			raw:     "rabbitmq-stream://app:2024/Sp/ring@broker:5552",
			parse:   parseAccepts,
			err:     fixedErr("multiple segments in URI path: /Sp/ring@broker:5552"),
			want:    "multiple segments in URI path: xxxxx:5552",
			secrets: []string{"2024", "Sp", "ring"},
		},
		{
			name:    "unquoted_dial_host_masked",
			raw:     "amqp://user:s3cr@zq9x?PW@broker:5672/vh",
			parse:   parseAccepts,
			err:     fixedErr("dial tcp: lookup zq9x: no such host"),
			want:    "dial tcp: lookup xxxxx: no such host",
			secrets: []string{"s3cr", "zq9x", "PW"},
		},
		{
			name:    "decoded_password_echo_masked",
			raw:     "amqp://user:pa%23ss@broker:5672/vh?token=zq9x",
			parse:   parseAccepts,
			err:     fixedErr("login refused for user:pa#ss (token=zq9x)"),
			want:    "login refused for user:xxxxx (xxxxx)",
			secrets: []string{"pa#ss", "pa%23ss", "pa", "ss", "zq9x"},
		},
		// A client rebuilds the URL around the decoded password; url.Parse cuts it at the '#'.
		{
			name:    "rebuilt_uri_cut_in_host",
			raw:     "rabbitmq-stream://user:s3cr%40%20zq9x%23PW@broker:5552/vh",
			parse:   parseAccepts,
			err:     func(string) error { return bareParseErr("rabbitmq-stream://user:s3cr@ zq9x#PW@broker:5552/vh") },
			want:    `parse "rabbitmq-stream://user:xxxxx@broker:5552/vh": invalid URL`,
			secrets: []string{"s3cr", "zq9x", "PW"},
		},
		{
			name:    "rebuilt_uri_cut_in_path",
			raw:     "rabbitmq-stream://app:3%2F3%25X%23PW@broker:5552/vh",
			parse:   parseAccepts,
			err:     func(string) error { return bareParseErr("rabbitmq-stream://app:3/3%X#PW@broker:5552/vh") },
			want:    `parse "rabbitmq-stream://app:xxxxx@broker:5552/vh": invalid URL`,
			secrets: []string{"3/3", "3%X", "PW"},
		},
		{
			name:    "vendor_word_not_in_raw_survives",
			raw:     "amqp://user:zq9x@broker:5672/vh",
			err:     fixedErr("Exception (403) Reason: ACCESS_REFUSED - Login was refused"),
			same:    true,
			secrets: []string{"zq9x"},
		},
		{
			name:    "host_and_port_tokens_survive",
			raw:     "amqp://user:zq9x@broker:5672/vh",
			err:     fixedErr("dial tcp broker:5672: connect: connection refused"),
			same:    true,
			secrets: []string{"zq9x"},
		},
		{name: "one_character_token_survives", raw: "amqp://user:zq9x@broker:5672/vh", err: fixedErr("frame 9 rejected"), same: true, secrets: []string{"zq9x"}},
		{name: "prose_word_inside_password_survives", raw: "amqp://user:Spring@broker:5672/vh", err: fixedErr("invalid control character in URL"), same: true, secrets: []string{"Spring"}},
		{
			name:    "prose_word_as_aligned_password_piece_masked",
			raw:     "amqp://user:2024/in@broker:5672/vh",
			parse:   parseAccepts,
			err:     fixedErr("invalid control character in URL"),
			want:    "invalid control character xxxxx URL",
			secrets: []string{" in ", "2024"},
		},
		{
			name:    "password_holding_the_placeholder_masked",
			raw:     "amqp://u:xxxxxyy@broker:5672/vh",
			parse:   parseAccepts,
			err:     fixedErr("login xxxxxyy refused"),
			want:    "login xxxxx refused",
			secrets: []string{"xxxxxyy"},
		},
		{name: "credential_free_url_leaves_vendor_text_alone", raw: "amqp://broker:5672/vh", err: fixedErr("dial tcp: lookup broker: no such host"), same: true},
		{
			name: "url_error_without_password",
			raw:  "amqp://broker:5672/vh",
			err: func(string) error {
				return &url.Error{Op: "parse", URL: "amqp://broker:5672/vh", Err: errors.New("boom")}
			},
			same: true,
		},
		// The masked port makes even a credential-free URL's quoting reason go.
		{name: "credential_free_url_drops_quoting_reason", raw: "amqp://broker:5672/vh%zz", parse: parseRejects, want: `parse "amqp://broker:xxxxx/vh%zz": invalid URL ` + hint, secrets: []string{"5672"}},
		// go-ora reads a wallet password from the DSN query.
		{
			name:    "url_error_query_masked_whole",
			raw:     "oracle://host:1521/XE?wallet_password=zq9x",
			err:     func(raw string) error { return &url.Error{Op: "parse", URL: raw, Err: errors.New("boom")} },
			want:    `parse "oracle://host:1521/XE?xxxxx": boom`,
			secrets: []string{"zq9x"},
		},
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
