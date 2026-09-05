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
	"crypto/rsa"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	gojose "github.com/go-jose/go-jose/v4"

	"github.com/gaborage/go-bricks/internal/keymaterial"
	jose "github.com/gaborage/go-bricks/jose"
)

// errUsage marks flag-parse failures whose message the FlagSet already
// printed to stderr itself — run must not print them a second time.
var errUsage = errors.New("usage error")

// cliConfig holds the parsed command-line configuration for one seal invocation.
type cliConfig struct {
	signKeyFile     string
	signKeyValue    string
	encryptKeyFile  string
	encryptKeyValue string
	signKid         string
	encryptKid      string
	sigAlg          string
	payloadPath     string // positional arg; "" or "-" means read stdin
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
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}

	err = validateConfig(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	signPriv, encPub, err := loadKeys(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	payload, err := readPayload(cfg, stdin)
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

	resolver := jose.NewKeyStoreResolver(&keymaterial.ProducerKeys{
		SignKid:    cfg.signKid,
		SignPriv:   signPriv,
		EncryptKid: cfg.encryptKid,
		EncPub:     encPub,
	})

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
	fs.StringVar(&cfg.signKeyFile, "sign-key-file", "",
		"path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) used to sign the outbound JWS")
	fs.StringVar(&cfg.signKeyValue, "sign-key-value", "",
		"base64-encoded DER RSA private key (alternative to -sign-key-file; argv is process-visible — fixture keys only)")
	fs.StringVar(&cfg.encryptKeyFile, "encrypt-key-file", "",
		"path to a DER-encoded RSA public key (PKIX) used to encrypt the outbound JWE")
	fs.StringVar(&cfg.encryptKeyValue, "encrypt-key-value", "",
		"base64-encoded DER RSA public key (alternative to -encrypt-key-file)")
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

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() > 1 {
		return nil, errors.New("expected at most one payload-file argument")
	}
	cfg.payloadPath = fs.Arg(0)
	return cfg, nil
}

// validateConfig enforces exactly-one-of per key source pair and required
// kids. It intentionally does NOT validate -sig-alg against an allowlist —
// that enforcement point is jose.Policy.Validate, called later in run.
func validateConfig(cfg *cliConfig) error {
	if !exactlyOne(cfg.signKeyFile, cfg.signKeyValue) {
		return errors.New("exactly one of -sign-key-file or -sign-key-value is required")
	}
	if !exactlyOne(cfg.encryptKeyFile, cfg.encryptKeyValue) {
		return errors.New("exactly one of -encrypt-key-file or -encrypt-key-value is required")
	}
	if cfg.signKid == "" {
		return errors.New("-sign-kid is required")
	}
	if cfg.encryptKid == "" {
		return errors.New("-encrypt-kid is required")
	}
	return nil
}

// exactlyOne reports whether precisely one of a, b is a non-empty string.
func exactlyOne(a, b string) bool {
	return (a != "") != (b != "")
}

// loadKeys resolves and parses the signing private key and encryption public
// key via internal/keymaterial — the same DER-loading mechanism the keystore
// module uses, so a key the CLI accepts is always one the middleware accepts.
func loadKeys(cfg *cliConfig) (signPriv *rsa.PrivateKey, encPub *rsa.PublicKey, err error) {
	signPriv, err = keymaterial.LoadRSAPrivateKey(cfg.signKeyFile, cfg.signKeyValue)
	if err != nil {
		return nil, nil, fmt.Errorf("sign key: %w", err)
	}

	encPub, err = keymaterial.LoadRSAPublicKey(cfg.encryptKeyFile, cfg.encryptKeyValue)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt key: %w", err)
	}

	return signPriv, encPub, nil
}

// readPayload reads the JSON payload from the positional file argument, or
// from stdin when the argument is absent or "-".
func readPayload(cfg *cliConfig, stdin io.Reader) ([]byte, error) {
	if cfg.payloadPath == "" || cfg.payloadPath == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}

	// #nosec G304,G703 -- operator-named CLI input file; reading it is this command's purpose
	data, err := os.ReadFile(cfg.payloadPath)
	if err != nil {
		return nil, fmt.Errorf("read payload file: %w", err)
	}
	return data, nil
}
