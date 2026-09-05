// Command seal-event seals one JSON object as a sealed AMQP event body using
// the go-bricks jose/sealed package's production SealDocument path, so a queue
// can be fed a real sealed message from the shell without writing a Go program.
//
// Install:
//
//	go install github.com/gaborage/go-bricks/cmd/seal-event@latest
//
// Usage:
//
//	echo '{"order_id":"o-1","card":{"pan":"4111111111111111"}}' | seal-event \
//	  -sign-key-file sign.der -encrypt-key-file enc.pub.der \
//	  -sign-kid svc-payments-sign-v1 -encrypt-kid aud-core-encrypt-v1 \
//	  -subject card -event-type payment.authorized -tenant-id t1
//
// The three bindings a consumer checks, each a rejection rather than a publish
// failure when it is wrong: both kids must be provisioned Generations
// ("<logical>-v<N>") of the families the consumer's seal tag names;
// -event-type must equal the consumer declaration's EventType, since the
// signed etyp is compared verbatim; and -tenant-id, which writes the signed
// tid, must equal the x-tenant-id header the message is published with under
// shared tenancy. See wiki/sealing.md for the full walkthrough.
package main

import (
	"crypto/rsa"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gaborage/go-bricks/internal/keymaterial"
	"github.com/gaborage/go-bricks/jose/sealed"
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
	subject         string
	eventType       string
	tenantID        string
	payloadPath     string // positional arg; "" or "-" means read stdin
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the thin orchestrator: parse flags, validate, derive the two Logical
// families from the concrete kids into a document Spec, load keys, read the
// document and seal. Every step after flag parsing writes one line to stderr
// and returns 1 on failure; flag.ErrHelp returns 0 and any other flag-parse
// error returns 2 (flag convention).
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

	// The Spec is built before any key is read and before stdin is drained: it is a pure
	// string check, so a mistyped kid should not cost two file reads and an RSA parse, and
	// must not leave an interactive operator blocked on a stdin that never closes.
	spec, err := documentSpec(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	signPriv, encPub, err := loadKeys(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	doc, err := readPayload(cfg, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	body, err := sealed.SealDocument(doc, spec, &sealed.Options{
		SignKid:    cfg.signKid,
		EncryptKid: cfg.encryptKid,
		EventType:  cfg.eventType,
		TenantID:   cfg.tenantID,
		Keys: &keymaterial.ProducerKeys{
			SignKid:    cfg.signKid,
			SignPriv:   signPriv,
			EncryptKid: cfg.encryptKid,
			EncPub:     encPub,
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintln(stdout, string(body))
	return 0
}

// parseFlags registers and parses the CLI flags. ContinueOnError (not
// ExitOnError) is load-bearing: ExitOnError would os.Exit from inside tests.
func parseFlags(args []string, stderr io.Writer) (*cliConfig, error) {
	fs := flag.NewFlagSet("seal-event", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := &cliConfig{}
	fs.StringVar(&cfg.signKeyFile, "sign-key-file", "",
		"path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) used to sign the sealed document")
	fs.StringVar(&cfg.signKeyValue, "sign-key-value", "",
		"base64-encoded DER RSA private key (alternative to -sign-key-file; argv is process-visible — fixture keys only)")
	fs.StringVar(&cfg.encryptKeyFile, "encrypt-key-file", "",
		"path to a DER-encoded RSA public key (PKIX) used to encrypt the subject member")
	fs.StringVar(&cfg.encryptKeyValue, "encrypt-key-value", "",
		"base64-encoded DER RSA public key (alternative to -encrypt-key-file)")
	fs.StringVar(&cfg.signKid, "sign-kid", "",
		"concrete sign generation written to the JWS header; must be a provisioned generation of the consumer's sign= family (required)")
	fs.StringVar(&cfg.encryptKid, "encrypt-kid", "",
		"concrete encrypt generation written to the JWE header; must be a provisioned generation of the consumer's encrypt= family (required)")
	fs.StringVar(&cfg.subject, "subject", "",
		"json member name of the subject — the one member sealed, and the signed sp entry (required)")
	fs.StringVar(&cfg.eventType, "event-type", "",
		"signed etyp; must equal the consumer declaration's EventType (required)")
	fs.StringVar(&cfg.tenantID, "tenant-id", "",
		"signed tid; under shared tenancy must equal the x-tenant-id header you publish with (optional)")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seal-event [flags] [payload-file]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Seals one JSON object as a sealed AMQP event body using go-bricks")
		fmt.Fprintln(stderr, "sealed.SealDocument, for feeding a queue from the shell.")
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

// validateConfig enforces exactly-one-of per key source pair (keymaterial's
// loaders let the file source win when both are set, so the choice has to be
// refused here) and the four required flags. Kid GRAMMAR is checked later, in
// documentSpec, where the family it derives is what the Spec needs.
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
	if cfg.subject == "" {
		return errors.New("-subject is required")
	}
	if cfg.eventType == "" {
		return errors.New("-event-type is required")
	}
	return nil
}

// exactlyOne reports whether precisely one of a, b is a non-empty string.
func exactlyOne(a, b string) bool {
	return (a != "") != (b != "")
}

// loadKeys resolves and parses the signing private key and the audience's
// encryption public key via internal/keymaterial — the same DER-loading
// mechanism the keystore module uses, so a key the CLI accepts is always one
// the consumer's keystore would accept.
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

// documentSpec derives each Logical family from its concrete Generation and
// builds the raw-document Spec. The wire carries the Generation while the
// Spec — like the consumer's seal tag — names the family, so the CLI takes the
// concrete kid and splits it rather than asking the operator for both.
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

// splitFamily reports the Logical family of a concrete kid, naming the flag
// that carried it so the operator knows which of the two to fix.
func splitFamily(flagName, kid string) (string, error) {
	family, _, ok := sealed.SplitGenerationKid(kid)
	if !ok {
		return "", fmt.Errorf("%s %q is not a generation: expected <logical>-v<N> with N a positive integer without leading zeros", flagName, kid)
	}
	return family, nil
}

// readPayload reads the JSON document from the positional file argument, or
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
