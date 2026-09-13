// Command open-event verifies and decrypts one sealed AMQP event body using the go-bricks
// jose/sealed package's production OpenDocument path, so an operator can inspect what a
// queue is carrying without writing a Go program.
//
// Install:
//
//	go install github.com/gaborage/go-bricks/cmd/open-event@latest
//
// Usage:
//
//	open-event -sign-key-file sign.pub.der -encrypt-key-file enc.der \
//	  -sign-kid svc-payments-sign-v1 -encrypt-kid aud-core-encrypt-v1 \
//	  -subject card -event-type payment.authorized \
//	  -tenancy shared -tenant-id t1 body.txt
//
// The sealed subject is NEVER printed by default: the document comes back with the subject
// member rendered as the string "<redacted>", so its place in the document stays visible
// and its size is not leaked. -print-subject is the fixture-only escape hatch.
//
// There is no skip-verification mode (ADR-097): the CLI fails exactly where the consume
// door fails, with the same SEAL_* code. Both wire kids are required flags and are never
// peeked from the unauthenticated protected header.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/gaborage/go-bricks/internal/sealcli"
	"github.com/gaborage/go-bricks/jose/sealed"
)

// Exit codes. Refusal (3) is deliberately apart from a tool error (1): a script wants to
// tell "this message is not admissible" from "this invocation could not run".
const (
	exitOK        = 0
	exitToolError = 1
	exitUsage     = 2
	exitRefused   = 3
)

// redactedValue is what the subject member's value becomes by default. It is a fixed-width
// literal: a length-proportional placeholder would leak the subject's size class.
const redactedValue = `"<redacted>"`

const subjectWarning = "warning: -print-subject renders the sealed subject in the clear — fixture data only"

// Tenancy modes, the consumer-side vocabulary messaging/sealed uses.
const (
	tenancyShared    = "shared"
	tenancyOptional  = "optional"
	tenancyPerTenant = "per-tenant"
	tenancyDisabled  = "disabled"
)

// cliConfig holds the parsed command-line configuration for one open invocation.
type cliConfig struct {
	keys         *sealcli.ConsumerKeySources
	signKid      string
	encryptKid   string
	subject      string
	eventType    string
	tenancy      string
	tenantID     string
	jsonOut      bool
	printSubject bool
	bodyPath     string // positional arg; "" or "-" means read stdin
}

// main exits with run's status so the shell sees 0 / 1 / 2 / 3.
func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the thin orchestrator: parse flags, derive the Spec and the tenant rule from them,
// load keys, read the body and open it. Flag-level failures are usage (2), key and input
// failures are tool errors (1), and a rule the message failed is a refusal (3).
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		if !errors.Is(err, sealcli.ErrUsage) {
			fmt.Fprintln(stderr, err)
		}
		return exitUsage
	}

	// Everything the flags alone decide is settled before any key is read and before stdin
	// is drained: a mistyped invocation costs no file read and never blocks an interactive
	// operator on a stdin that will not close.
	spec, tenant, err := openPlan(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	keys, err := cfg.keys.Load(cfg.signKid, cfg.encryptKid)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitToolError
	}

	body, err := sealcli.ReadPayloadCapped(cfg.bodyPath, stdin, sealcli.MaxPayloadBytes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitToolError
	}

	// A shell redirect leaves a trailing newline; the compact serialization does not carry one.
	opened, err := sealed.OpenDocument(bytes.TrimSpace(body), spec, &sealed.OpenOptions{
		EventType: cfg.eventType,
		Tenant:    tenant,
		Keys:      keys,
	})
	if err != nil {
		return reportRefusal(cfg, err, stdout, stderr)
	}

	return emit(cfg, opened, stdout, stderr)
}

// parseFlags registers and parses the CLI flags. ContinueOnError (not ExitOnError) is
// load-bearing: ExitOnError would os.Exit from inside tests.
func parseFlags(args []string, stderr io.Writer) (*cliConfig, error) {
	fs := flag.NewFlagSet("open-event", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := &cliConfig{}
	cfg.keys = sealcli.ConsumerKeyFlags(fs,
		"used to verify the sealed document's signature",
		"used to decrypt the subject member")
	fs.StringVar(&cfg.signKid, "sign-kid", "",
		"concrete sign generation the body must carry; never peeked from the unauthenticated header (required)")
	fs.StringVar(&cfg.encryptKid, "encrypt-kid", "",
		"concrete encrypt generation the inner JWE must carry (required)")
	fs.StringVar(&cfg.subject, "subject", "",
		"json member name of the subject — the one sealed member, and the signed sp entry (required)")
	fs.StringVar(&cfg.eventType, "event-type", "",
		"the consumer declaration's EventType; the signed etyp is compared verbatim (required)")
	fs.StringVar(&cfg.tenancy, "tenancy", tenancyDisabled,
		"tid rule to apply: shared | per-tenant | optional | disabled: tenant stamp not judged, and -tenant-id is then refused")
	fs.StringVar(&cfg.tenantID, "tenant-id", "",
		"the tenant a present signed tid must equal; empty checks presence only, and it cannot be combined with -tenancy disabled, which judges no tid at all")
	fs.BoolVar(&cfg.jsonOut, "json", false,
		"emit {envelope,document} on success and {code,details} on refusal, instead of text")
	fs.BoolVar(&cfg.printSubject, "print-subject", false,
		"render the decrypted subject in the clear instead of \"<redacted>\" — FIXTURE DATA ONLY")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: open-event [flags] [body-file]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Verifies and decrypts one sealed AMQP event body using go-bricks")
		fmt.Fprintln(stderr, "sealed.OpenDocument, printing its envelope and the surrounding")
		fmt.Fprintln(stderr, "document with the sealed subject redacted.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "body-file is a path to a sealed body, or '-'/absent to read stdin.")
		fmt.Fprintf(stderr, "At most %d bytes are read from either; a larger input is refused.\n", sealcli.MaxPayloadBytes)
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Exit codes: 0 opened, 1 tool error, 2 usage, 3 refused.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}

	path, err := sealcli.PositionalPath(fs, args)
	if err != nil {
		return nil, err
	}
	cfg.bodyPath = path
	return cfg, nil
}

// openPlan turns the validated flags into the two values OpenDocument needs beyond the keys.
// Every failure here is a usage error: nothing has touched the filesystem yet.
func openPlan(cfg *cliConfig) (*sealed.Spec, sealed.TenantExpectation, error) {
	var noTenant sealed.TenantExpectation

	if err := validateConfig(cfg); err != nil {
		return nil, noTenant, err
	}
	spec, err := documentSpec(cfg)
	if err != nil {
		return nil, noTenant, err
	}
	tenant, err := tenantExpectation(cfg.tenancy, cfg.tenantID)
	if err != nil {
		return nil, noTenant, err
	}
	return spec, tenant, nil
}

// validateConfig enforces exactly-one-of per key source pair (delegated to sealcli, which
// owns the refusal strings) and the four required flags. Kid GRAMMAR is checked in
// documentSpec, where the family it derives is what the Spec needs.
func validateConfig(cfg *cliConfig) error {
	if err := cfg.keys.Validate(); err != nil {
		return err
	}
	required := []struct{ name, value string }{
		{"-sign-kid", cfg.signKid},
		{"-encrypt-kid", cfg.encryptKid},
		{"-subject", cfg.subject},
		{"-event-type", cfg.eventType},
	}
	for _, r := range required {
		if r.value == "" {
			return fmt.Errorf("%s is required", r.name)
		}
	}
	return nil
}

// documentSpec derives each Logical family from its concrete Generation and builds the
// raw-document Spec, exactly as seal-event does: the wire carries the Generation while the
// Spec names the family, so the CLI takes the concrete kid and splits it.
func documentSpec(cfg *cliConfig) (*sealed.Spec, error) {
	signFamily, err := splitFamily("-sign-kid", cfg.signKid)
	if err != nil {
		return nil, err
	}
	encryptFamily, err := splitFamily("-encrypt-kid", cfg.encryptKid)
	if err != nil {
		return nil, err
	}
	return sealed.NewDocumentSpec(signFamily, encryptFamily, cfg.subject)
}

// splitFamily reports the Logical family of a concrete kid, naming the flag that carried it.
func splitFamily(flagName, kid string) (string, error) {
	family, _, ok := sealed.SplitGenerationKid(kid)
	if !ok {
		return "", fmt.Errorf("%s %q is not a generation: expected <logical>-v<N> with N a positive integer without leading zeros", flagName, kid)
	}
	return family, nil
}

// tenantExpectation maps the consumer tenancy vocabulary onto the tid rule, the same way
// messaging/sealed maps it for a live delivery:
//
//   - shared: a signed tid is REQUIRED and, when -tenant-id is given, must equal it — the
//     flag stands in for the x-tenant-id carrier the delivery pipeline would have read;
//   - optional and per-tenant: the SAME rule, {Expected: tenantID} — an absent tid is
//     accepted and a present one that differs is poison. Four mode names spell three
//     behaviors: these two differ from shared only in Required, and keeping both names is
//     what lets an operator say which deployment they are reproducing;
//   - disabled: no rule at all; the tid is surfaced on the envelope only, which is why a
//     -tenant-id nobody would judge is refused rather than silently ignored.
func tenantExpectation(tenancy, tenantID string) (sealed.TenantExpectation, error) {
	switch tenancy {
	case tenancyShared:
		return sealed.TenantExpectation{Required: true, Expected: tenantID}, nil
	case tenancyOptional, tenancyPerTenant:
		return sealed.TenantExpectation{Expected: tenantID}, nil
	case tenancyDisabled:
		if tenantID != "" {
			return sealed.TenantExpectation{}, errors.New("-tenant-id cannot be combined with -tenancy disabled, which applies no tid rule; pick shared, optional or per-tenant")
		}
		return sealed.TenantExpectation{}, nil
	default:
		return sealed.TenantExpectation{}, fmt.Errorf("-tenancy %q is not one of shared, per-tenant, optional, disabled", tenancy)
	}
}

// reportRefusal renders a refused message: the wire code and its presence/length details,
// on stdout under -json (so a pipeline reads one stream) and on stderr otherwise. Rule is
// omitted from both — its numbering is documented unstable. Anything that is not an
// *OpenError never came from the rule chain and is a tool error.
func reportRefusal(cfg *cliConfig, err error, stdout, stderr io.Writer) int {
	var oe *sealed.OpenError
	if !errors.As(err, &oe) {
		fmt.Fprintln(stderr, err)
		return exitToolError
	}

	if !cfg.jsonOut {
		fmt.Fprintln(stderr, oe.Error())
		return exitRefused
	}

	details := oe.Details
	if details == nil {
		details = map[string]string{}
	}
	if encErr := writeJSON(stdout, jsonRefusal{Code: oe.Err.Code, Details: details}); encErr != nil {
		fmt.Fprintln(stderr, encErr)
		return exitToolError
	}
	return exitRefused
}

// emit renders an opened message. The -print-subject warning goes out first, so an operator
// watching a terminal sees it above the plaintext it is about.
func emit(cfg *cliConfig, opened *sealed.OpenedDocument, stdout, stderr io.Writer) int {
	value := []byte(redactedValue)
	if cfg.printSubject {
		fmt.Fprintln(stderr, subjectWarning)
		value = opened.Subject
	}
	doc := spliceMember(opened.Document, opened.SubjectAt, cfg.subject, value)

	if cfg.jsonOut {
		if err := writeJSON(stdout, jsonOpened{Envelope: newJSONEnvelope(opened.Envelope), Document: doc}); err != nil {
			fmt.Fprintln(stderr, err)
			return exitToolError
		}
		return exitOK
	}

	writeEnvelope(stdout, opened.Envelope)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, string(doc))
	return exitOK
}

// spliceMember puts the subject member back where OpenDocument removed it, with value as
// its JSON value. removeMember took exactly one adjacent separator with the member, so the
// separator this puts back is the one the surviving neighbors need: a following key means
// the member was first and owned the comma after it; a following comma means it had a
// leading one; and with nothing following, only a non-empty object still needs a comma.
func spliceMember(doc []byte, at int, name string, value []byte) []byte {
	key, _ := json.Marshal(name) // a Go string always marshals
	member := slices.Concat(key, []byte(":"), value)

	rest := bytes.TrimLeft(doc[at:], " \t\r\n")
	switch {
	case bytes.HasPrefix(rest, []byte(`"`)):
		member = append(member, ',')
	case bytes.HasPrefix(rest, []byte(",")):
		member = slices.Concat([]byte(","), member)
	case !bytes.HasSuffix(bytes.TrimRight(doc[:at], " \t\r\n"), []byte("{")):
		member = slices.Concat([]byte(","), member)
	}
	return slices.Concat(doc[:at], member, doc[at:])
}

// jsonEnvelope is the -json envelope shape: every Envelope field, and nothing else.
type jsonEnvelope struct {
	JTI        string `json:"jti"`
	IssuedAt   string `json:"issuedAt"`
	EventType  string `json:"eventType"`
	TenantID   string `json:"tenantId"`
	SignKid    string `json:"signKid"`
	SignFamily string `json:"signFamily"`
	EncKid     string `json:"encKid"`
}

// jsonOpened is the -json success payload; Document is embedded as JSON, not as a quoted
// string a caller would have to decode a second time.
type jsonOpened struct {
	Envelope jsonEnvelope    `json:"envelope"`
	Document json.RawMessage `json:"document"`
}

// jsonRefusal is the -json refusal payload. Details carry presence, length and layer only.
type jsonRefusal struct {
	Code    string            `json:"code"`
	Details map[string]string `json:"details"`
}

func newJSONEnvelope(env *sealed.Envelope) jsonEnvelope {
	return jsonEnvelope{
		JTI:        env.JTI,
		IssuedAt:   issuedAt(env),
		EventType:  env.EventType,
		TenantID:   env.TenantID,
		SignKid:    env.SignKid,
		SignFamily: env.SignFamily,
		EncKid:     env.EncKid,
	}
}

// issuedAt renders the signed seal time in UTC. It is informational: no rule judged it.
func issuedAt(env *sealed.Envelope) string {
	return env.IssuedAt.UTC().Format(time.RFC3339)
}

// writeJSON encodes one payload with the encoder's default HTML escaping left ON. The
// redaction placeholder therefore travels as "\u003credacted\u003e", which every decoder
// reads back as <redacted>; turning escaping off would ship a subject carrying <script>
// verbatim into a browser-backed DLQ viewer.
func writeJSON(w io.Writer, payload any) error {
	return json.NewEncoder(w).Encode(payload)
}

// writeEnvelope renders the envelope as aligned label lines above the document.
func writeEnvelope(w io.Writer, env *sealed.Envelope) {
	fields := []struct{ label, value string }{
		{"JTI", env.JTI},
		{"IssuedAt", issuedAt(env)},
		{"EventType", env.EventType},
		{"TenantID", env.TenantID},
		{"SignKid", env.SignKid},
		{"SignFamily", env.SignFamily},
		{"EncKid", env.EncKid},
	}
	for _, f := range fields {
		fmt.Fprintf(w, "%-11s %s\n", f.label+":", f.value)
	}
}
