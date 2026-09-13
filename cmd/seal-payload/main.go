// Command seal-payload seals a JSON payload as a compact JWE-of-JWS token
// using the go-bricks jose package's production Seal path, so a jose-tagged
// endpoint can be exercised from curl without hand-writing a Go program.
//
// Install:
//
//	go install github.com/gaborage/go-bricks/cmd/seal-payload@latest
//
// Usage:
//
//	seal-payload -sign-key-file sign.der -encrypt-key-file enc.pub.der \
//	  -sign-kid visa-vts-verify -encrypt-kid our-signing payload.json
//
// -sign-kid must equal the target endpoint's jose "verify=" tag name, and
// -encrypt-kid must equal its "decrypt=" tag name — the server binds kid
// headers to the policy's configured kids and rejects a mismatch with
// JOSE_KID_UNKNOWN.
//
// -mode bare emits a single JWE with nothing signed (Visa Message Level
// Encryption): it takes only the encryption key and -encrypt-kid, refuses
// every signing flag, and unlocks -typ, -iat-ms, -protected and -enc A128GCM.
// -envelope visa-mle prints {"encData":"<compact>"} instead of the bare token.
// See wiki/jose.md for the full walkthrough.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/internal/sealcli"
	jose "github.com/gaborage/go-bricks/jose"
)

// cliConfig holds the parsed command-line configuration for one seal invocation.
type cliConfig struct {
	keys         *sealcli.KeySources
	signKid      string
	encryptKid   string
	sigAlg       string
	mode         string
	enc          string
	envelope     string
	typ          string
	typSet       bool // -typ was explicitly supplied, even as -typ="" (nested refusal is presence-based)
	iatMillis    bool
	iatMillisSet bool // -iat-ms was explicitly supplied, even as -iat-ms=false (nested refusal is presence-based)
	protected    protectedFlag
	payloadPath  string // positional arg; "" or "-" means read stdin
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the thin orchestrator: parse flags, validate, build and validate the
// policy, load keys, read the payload, seal, and report. Every step after flag
// parsing writes one line to stderr and returns 1 on failure; flag.ErrHelp
// returns 0 and any other flag-parse error returns 2 (flag convention).
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if !errors.Is(err, sealcli.ErrUsage) {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}

	mode, err := resolveMode(cfg.mode)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg.keys.SignOptional = mode == jose.SealModeBareJWE

	err = validateConfig(cfg, mode)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// The policy needs no keys, so a policy refusal costs no key read.
	p := buildPolicy(cfg, mode)
	err = p.Validate()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	keys, err := cfg.keys.Load(cfg.signKid, cfg.encryptKid)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	payload, err := sealcli.ReadPayload(cfg.payloadPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	compact, err := jose.Seal(payload, p, jose.NewKeyStoreResolver(keys))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	out, err := wrapEnvelope(cfg.envelope, compact)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

// parseFlags registers and parses the CLI flags. ContinueOnError (not
// ExitOnError) is load-bearing: ExitOnError would os.Exit from inside tests.
func parseFlags(args []string, stderr io.Writer) (*cliConfig, error) {
	fs := flag.NewFlagSet("seal-payload", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := &cliConfig{}
	cfg.keys = sealcli.KeyFlags(fs, "used to sign the outbound JWS (nested mode only)", "used to encrypt the outbound JWE")
	fs.StringVar(&cfg.signKid, "sign-kid", "",
		"kid embedded in the JWS header; must equal the target endpoint's verify= tag name (required with -mode nested; refused with -mode bare)")
	fs.StringVar(&cfg.encryptKid, "encrypt-kid", "",
		"kid embedded in the JWE header; must equal the target endpoint's decrypt= tag name (required)")
	fs.StringVar(&cfg.sigAlg, "sig-alg", "",
		"JWS signature algorithm: RS256 or PS256 (nested default "+string(jose.DefaultSigAlg)+")")
	fs.StringVar(&cfg.mode, "mode", modeNested,
		"wire shape: nested (JWE of a signed JWS) or bare (JWE only, nothing signed — Visa Message Level Encryption)")
	fs.StringVar(&cfg.enc, "enc", string(jose.DefaultEnc), fmt.Sprintf(
		"JWE content encryption; nested allows %v, bare allows %v",
		jose.AllowedContentEncsFor(jose.SealModeJWEofJWS), jose.AllowedContentEncsFor(jose.SealModeBareJWE)))
	fs.StringVar(&cfg.typ, "typ", "", "JWE protected typ header, e.g. JOSE (-mode bare only)")
	fs.BoolVar(&cfg.iatMillis, "iat-ms", false,
		"stamp an iat protected header in Unix epoch MILLISECONDS at seal time (-mode bare only)")
	fs.Var(&cfg.protected, "protected",
		"extra JWE protected header as `key=value`, string value; repeatable (-mode bare only)")
	fs.StringVar(&cfg.envelope, "envelope", "",
		`wrap the compact token in a body envelope; only visa-mle ({"encData":"<compact>"}) is supported`)

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seal-payload [flags] [payload-file]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Seals a JSON payload as a compact JWE-of-JWS token (or, with -mode bare, a bare JWE)")
		fmt.Fprintln(stderr, "using go-bricks jose.Seal, for curl-testing jose-protected endpoints.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "payload-file is a path to a JSON file, or '-'/absent to read stdin.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}

	path, err := sealcli.PositionalPath(fs, args)
	if err != nil {
		return nil, err
	}
	cfg.payloadPath = path

	// fs.Visit only calls back for flags actually given on the command line, so
	// this records presence even for an explicit zero value (-typ="",
	// -iat-ms=false) that Parse would otherwise leave indistinguishable from
	// "not supplied" — refuseNestedHeaderFlags needs presence, not value.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "typ":
			cfg.typSet = true
		case "iat-ms":
			cfg.iatMillisSet = true
		}
	})
	return cfg, nil
}

// validateConfig checks cfg against the resolved mode without changing it:
// signing flags are refused under bare mode and bare-only header flags under
// nested mode, each by name; key sources are exactly-one-of per pair
// (delegated to sealcli, which owns the refusal strings and reads the
// SignOptional run set); the required kids; -enc against the mode's allowlist,
// checked here because jose's own refusal would not name the allowed set; and
// -envelope. -sig-alg is left to jose.Policy.Validate, called next in run.
func validateConfig(cfg *cliConfig, mode jose.SealMode) error {
	if err := refuseBareSigning(cfg, mode); err != nil {
		return err
	}
	if err := refuseNestedHeaderFlags(cfg, mode); err != nil {
		return err
	}
	if err := cfg.keys.Validate(); err != nil {
		return err
	}
	if mode == jose.SealModeJWEofJWS && cfg.signKid == "" {
		return errors.New("-sign-kid is required")
	}
	if cfg.encryptKid == "" {
		return errors.New("-encrypt-kid is required")
	}
	if allowed := jose.AllowedContentEncsFor(mode); !jose.IsAllowedEncFor(mode, gojose.ContentEncryption(cfg.enc)) {
		return fmt.Errorf("-enc %q is not allowed with -mode %s (allowed: %v)", cfg.enc, cfg.mode, allowed)
	}
	if cfg.envelope != "" && cfg.envelope != envelopeVisaMLE {
		return fmt.Errorf("-envelope %q is not supported (only %s)", cfg.envelope, envelopeVisaMLE)
	}
	return nil
}

// refuseNestedHeaderFlags names the first bare-only header flag supplied under -mode nested.
func refuseNestedHeaderFlags(cfg *cliConfig, mode jose.SealMode) error {
	if mode != jose.SealModeJWEofJWS {
		return nil
	}
	headers := []struct {
		flag string
		set  bool
	}{
		{"-typ", cfg.typSet},
		{"-iat-ms", cfg.iatMillisSet},
		{"-protected", cfg.protected != nil},
	}
	for _, h := range headers {
		if h.set {
			return fmt.Errorf("%s requires -mode bare", h.flag)
		}
	}
	return nil
}

// refuseBareSigning names the first signing input supplied under -mode bare.
func refuseBareSigning(cfg *cliConfig, mode jose.SealMode) error {
	if mode != jose.SealModeBareJWE {
		return nil
	}
	signing := []struct{ flag, value string }{
		{"-sign-key-file", cfg.keys.SignFile},
		{"-sign-key-value", cfg.keys.SignValue},
		{"-sign-kid", cfg.signKid},
		{"-sig-alg", cfg.sigAlg},
	}
	for _, s := range signing {
		if s.value != "" {
			return fmt.Errorf("%s is not accepted with -mode bare: a bare JWE carries no signature", s.flag)
		}
	}
	return nil
}

const envelopeVisaMLE = "visa-mle"

// visaMLEBody mirrors httpclient's Visa MLE wire object without importing httpclient.
type visaMLEBody struct {
	EncData string `json:"encData"`
}

// wrapEnvelope returns compact unchanged, or wrapped in the named envelope.
func wrapEnvelope(envelope, compact string) (string, error) {
	switch envelope {
	case "":
		return compact, nil
	case envelopeVisaMLE:
		body, _ := json.Marshal(visaMLEBody{EncData: compact}) // a lone string field cannot fail to marshal
		return string(body), nil
	default:
		return "", fmt.Errorf("-envelope %q is not supported (only %s)", envelope, envelopeVisaMLE)
	}
}

const (
	modeNested = "nested"
	modeBare   = "bare"
)

// resolveMode maps a -mode value onto a jose.SealMode.
func resolveMode(name string) (jose.SealMode, error) {
	switch name {
	case modeNested:
		return jose.SealModeJWEofJWS, nil
	case modeBare:
		return jose.SealModeBareJWE, nil
	default:
		return jose.SealModeJWEofJWS, fmt.Errorf("-mode %q is not one of %s or %s", name, modeNested, modeBare)
	}
}

// buildPolicy assembles the outbound policy; only nested mode signs, so only it gets a SigAlg.
func buildPolicy(cfg *cliConfig, mode jose.SealMode) *jose.Policy {
	p := &jose.Policy{
		Direction:        jose.DirectionOutbound,
		Mode:             mode,
		SignKid:          cfg.signKid,
		EncryptKid:       cfg.encryptKid,
		KeyAlg:           jose.DefaultKeyAlg,
		Enc:              gojose.ContentEncryption(cfg.enc),
		Cty:              jose.DefaultCty,
		Typ:              cfg.typ,
		IATMillis:        cfg.iatMillis,
		ProtectedHeaders: cfg.protected, // nil unless -protected was given
	}
	if mode == jose.SealModeJWEofJWS {
		p.SigAlg = jose.DefaultSigAlg
		if cfg.sigAlg != "" {
			p.SigAlg = gojose.SignatureAlgorithm(cfg.sigAlg)
		}
	}
	return p
}

// protectedFlag collects repeatable -protected key=value pairs as string header values.
type protectedFlag map[string]any

func (f *protectedFlag) String() string { return "" }

func (f *protectedFlag) Set(pair string) error {
	key, value, ok := strings.Cut(pair, "=")
	if !ok || key == "" || strings.ContainsFunc(key, invalidHeaderKeyRune) {
		return fmt.Errorf("want key=value with a key free of whitespace and control characters, got %q", pair)
	}
	if *f == nil {
		*f = protectedFlag{}
	}
	if _, dup := (*f)[key]; dup {
		return fmt.Errorf("header %q given more than once", key)
	}
	(*f)[key] = value
	return nil
}

func invalidHeaderKeyRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}
