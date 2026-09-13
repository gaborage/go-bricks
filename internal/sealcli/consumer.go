package sealcli

import (
	"flag"

	"github.com/gaborage/go-bricks/internal/keymaterial"
)

// ConsumerKeySources holds the four key-source flags an opening CLI takes. It
// is the inverse-role twin of KeySources: the same four flag NAMES, carrying
// the mirror halves — the producer's sign PUBLIC key and the audience's own
// encrypt PRIVATE key.
type ConsumerKeySources struct {
	SignFile, SignValue, EncryptFile, EncryptValue string
}

// ConsumerKeyFlags registers the four key-source flags on fs in the consumer
// roles and returns the struct they bind to. verifyUse and decryptUse are the
// per-CLI purpose clauses appended to the two -key-file help strings, the same
// way KeyFlags takes them.
func ConsumerKeyFlags(fs *flag.FlagSet, verifyUse, decryptUse string) *ConsumerKeySources {
	k := &ConsumerKeySources{}
	fs.StringVar(&k.SignFile, "sign-key-file", "",
		"path to a DER-encoded RSA public key (PKIX) "+verifyUse)
	fs.StringVar(&k.SignValue, "sign-key-value", "",
		"base64-encoded DER RSA public key (alternative to -sign-key-file)")
	fs.StringVar(&k.EncryptFile, "encrypt-key-file", "",
		"path to a DER-encoded RSA private key (PKCS#8 or PKCS#1) "+decryptUse)
	fs.StringVar(&k.EncryptValue, "encrypt-key-value", "",
		"base64-encoded DER RSA private key (alternative to -encrypt-key-file; argv is process-visible — fixture keys only)")
	return k
}

// Validate enforces exactly-one-of per key-source pair, refusing with the same
// strings the producer pair uses so an operator reads one vocabulary across
// both binaries.
func (k *ConsumerKeySources) Validate() error {
	return validateKeyPair(k.SignFile, k.SignValue, k.EncryptFile, k.EncryptValue)
}

// Load re-runs Validate, then loads and parses both keys and returns the
// consumer-role resolver under the given kids. The refusals precede any I/O.
func (k *ConsumerKeySources) Load(signKid, encryptKid string) (*keymaterial.ConsumerKeys, error) {
	signPub, encPriv, err := loadKeyPair(k.SignFile, k.SignValue, k.EncryptFile, k.EncryptValue,
		keymaterial.LoadRSAPublicKey, keymaterial.LoadRSAPrivateKey)
	if err != nil {
		return nil, err
	}
	return &keymaterial.ConsumerKeys{
		SignKid:    signKid,
		SignPub:    signPub,
		EncryptKid: encryptKid,
		EncPriv:    encPriv,
	}, nil
}
