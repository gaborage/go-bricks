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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gaborage/go-bricks/internal/sealcli"
	"github.com/gaborage/go-bricks/jose/sealed"
)

// cliConfig holds the parsed command-line configuration for one seal invocation.
type cliConfig struct {
	keys        *sealcli.KeySources
	signKid     string
	encryptKid  string
	subject     string
	eventType   string
	tenantID    string
	payloadPath string // positional arg; "" or "-" means read stdin
}

// main exits with run's status so the shell sees 0 / 1 / 2.
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

	// The Spec is built before any key is read and before stdin is drained: it is a pure
	// string check, so a mistyped kid should not cost two file reads and an RSA parse, and
	// must not leave an interactive operator blocked on a stdin that never closes.
	spec, err := documentSpec(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	keys, err := cfg.keys.Load(cfg.signKid, cfg.encryptKid)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	doc, err := sealcli.ReadPayload(cfg.payloadPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	body, err := sealed.SealDocument(doc, spec, &sealed.Options{
		SignKid:    cfg.signKid,
		EncryptKid: cfg.encryptKid,
		EventType:  cfg.eventType,
		TenantID:   cfg.tenantID,
		Keys:       keys,
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
	cfg.keys = sealcli.KeyFlags(fs, "used to sign the sealed document", "used to encrypt the subject member")
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

	path, err := sealcli.PositionalPath(fs, args)
	if err != nil {
		return nil, err
	}
	cfg.payloadPath = path
	return cfg, nil
}

// validateConfig enforces exactly-one-of per key source pair (delegated to
// sealcli, which owns the refusal strings) and the four required flags. Kid
// GRAMMAR is checked later, in documentSpec, where the family it derives is
// what the Spec needs.
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
	if cfg.subject == "" {
		return errors.New("-subject is required")
	}
	if cfg.eventType == "" {
		return errors.New("-event-type is required")
	}
	return nil
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
