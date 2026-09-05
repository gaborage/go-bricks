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
// JOSE_KID_UNKNOWN. See wiki/jose.md for the full walkthrough.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/internal/sealcli"
	jose "github.com/gaborage/go-bricks/jose"
)

// cliConfig holds the parsed command-line configuration for one seal invocation.
type cliConfig struct {
	keys        *sealcli.KeySources
	signKid     string
	encryptKid  string
	sigAlg      string
	payloadPath string // positional arg; "" or "-" means read stdin
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the thin orchestrator: parse flags, validate, load keys, read the
// payload, seal, and report. Every step after flag parsing writes one line
// to stderr and returns 1 on failure; flag.ErrHelp returns 0 and any other
// flag-parse error returns 2 (flag convention).
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

	err = validateConfig(cfg)
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

	p := &jose.Policy{
		Direction:  jose.DirectionOutbound,
		SignKid:    cfg.signKid,
		EncryptKid: cfg.encryptKid,
		SigAlg:     gojose.SignatureAlgorithm(cfg.sigAlg),
		KeyAlg:     jose.DefaultKeyAlg,
		Enc:        jose.DefaultEnc,
		Cty:        jose.DefaultCty,
	}
	err = p.Validate()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	resolver := jose.NewKeyStoreResolver(keys)

	compact, err := jose.Seal(payload, p, resolver)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintln(stdout, compact)
	return 0
}

// parseFlags registers and parses the CLI flags. ContinueOnError (not
// ExitOnError) is load-bearing: ExitOnError would os.Exit from inside tests.
func parseFlags(args []string, stderr io.Writer) (*cliConfig, error) {
	fs := flag.NewFlagSet("seal-payload", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := &cliConfig{}
	cfg.keys = sealcli.KeyFlags(fs, "used to sign the outbound JWS", "used to encrypt the outbound JWE")
	fs.StringVar(&cfg.signKid, "sign-kid", "",
		"kid embedded in the JWS header; must equal the target endpoint's verify= tag name (required)")
	fs.StringVar(&cfg.encryptKid, "encrypt-kid", "",
		"kid embedded in the JWE header; must equal the target endpoint's decrypt= tag name (required)")
	fs.StringVar(&cfg.sigAlg, "sig-alg", string(jose.DefaultSigAlg), "JWS signature algorithm: RS256 or PS256")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seal-payload [flags] [payload-file]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Seals a JSON payload as a compact JWE-of-JWS token using go-bricks jose.Seal,")
		fmt.Fprintln(stderr, "for curl-testing jose-tagged endpoints.")
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
	return cfg, nil
}

// validateConfig enforces exactly-one-of per key source pair (delegated to
// sealcli, which owns the refusal strings) and the required kids. It
// intentionally does NOT validate -sig-alg against an allowlist — that
// enforcement point is jose.Policy.Validate, called later in run.
func validateConfig(cfg *cliConfig) error {
	if err := cfg.keys.Validate(); err != nil {
		return err
	}
	if cfg.signKid == "" {
		return errors.New("-sign-kid is required")
	}
	if cfg.encryptKid == "" {
		return errors.New("-encrypt-kid is required")
	}
	return nil
}
