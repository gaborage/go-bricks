package sealed

// The raw-document door. Seal takes a Go value and lets encoding/json produce the bytes;
// a tool (cmd/seal-event) or a JSON-fixture test has the bytes already and no Go type at
// all. Both doors share one core, one envelope and one set of invariants — the difference
// is only where the document came from, and the typed door stays the production path.

// NewDocumentSpec builds the Spec of a document that no Go type describes: the Two-kid
// identity and the json member name of the Subject. Both kids must be Logical kids
// (CheckLogicalKid: `^[A-Za-z0-9_-]+$`, at most MaxLogicalKidLen, no `-v<digits>` suffix)
// and subjectPath must be non-empty; failures carry the tag scan's own codes,
// SEAL_TAG_KID_INVALID with the offending Kid and SEAL_TAG_SUBJECT_MISSING.
//
// The returned Spec has a nil Type and an empty SubjectField, so it opens no door but
// SealDocument: Seal and Open both require a Spec from ScanType and refuse it with
// SEAL_OPTIONS_INVALID.
func NewDocumentSpec(signLogical, encryptLogical, subjectPath string) (*Spec, error) {
	if err := checkSentinelKid(tagKeySign, signLogical); err != nil {
		return nil, err
	}
	if err := checkSentinelKid(tagKeyEncrypt, encryptLogical); err != nil {
		return nil, err
	}
	if subjectPath == "" {
		return nil, tagError(CodeTagSubjectMissing, "a document spec needs a non-empty subject path")
	}
	return &Spec{SignLogical: signLogical, EncryptLogical: encryptLogical, SubjectPath: subjectPath}, nil
}

// SealDocument seals a document the caller already serialized. It is the byte-level twin of
// Seal: same envelope, same protected header set, same fresh `jti` minted here and `iat`
// from opts.Now. spec may come from NewDocumentSpec or from ScanType — SealDocument never
// looks at spec.Type.
//
// The signed payload is doc byte for byte with one substitution: the Subject member's value
// becomes the compact JWE. Member order, whitespace and number formatting are the caller's
// and travel unchanged, which is what makes a hand-written fixture reproducible.
//
// doc is refused with SEAL_DOCUMENT_INVALID when it is not a single JSON object, carries
// trailing content, lacks the Subject member, carries it twice, or carries a top-level
// member whose name case-folds to the Subject's without equalling it (the G9 rule, which
// encoding/json's case-insensitive decode would otherwise let a consumer read instead of
// the sealed member). Nothing of the document's bytes reaches the error.
//
// SealDocument exists for tooling and JSON-fixture tests. Producers seal events with Seal,
// whose scanned Spec pins the Go type the wire must match.
func SealDocument(doc []byte, spec *Spec, opts *Options) ([]byte, error) {
	if err := opts.Validate(spec); err != nil {
		return nil, err
	}
	return sealCore(doc, spec, opts)
}
