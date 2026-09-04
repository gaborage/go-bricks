package sealed

import "errors"

// Sentinel errors. Every failure surfaces as a *jose.Error whose Sentinel is one of these
// (or, for key resolution, the resolver's own *jose.Error verbatim), so callers use
// errors.Is against the sentinel and read the Code for the exact rule that fired.
var (
	// ErrTagInvalid is the scan-time sentinel: a `seal` declaration the framework refuses.
	ErrTagInvalid = errors.New("sealed: invalid seal struct tag")
	// ErrKidFamilyMismatch fires when a concrete kid is not a Generation of the declared
	// Logical kid — at seal time for the options, at open time for the wire.
	ErrKidFamilyMismatch = errors.New("sealed: kid is not a generation of the declared family")
	// ErrSealFailed covers every sealer-side failure that is not a tag or family error:
	// invalid options, a document the splice cannot pin, or a crypto primitive failing.
	ErrSealFailed = errors.New("sealed: seal failed")
)

// Wire-protocol error codes. Scan codes are startup errors; the rest are runtime.
const (
	CodeTagInvalid         = "SEAL_TAG_INVALID"
	CodeTagKidInvalid      = "SEAL_TAG_KID_INVALID"
	CodeTagSentinelMissing = "SEAL_TAG_SENTINEL_MISSING"
	CodeTagSubjectMissing  = "SEAL_TAG_SUBJECT_MISSING"
	CodeTagSubjectMultiple = "SEAL_TAG_SUBJECT_MULTIPLE"
	CodeTagSubjectInvalid  = "SEAL_TAG_SUBJECT_INVALID"
	CodeKidFamilyMismatch  = "SEAL_KID_FAMILY_MISMATCH"
	CodeOptionsInvalid     = "SEAL_OPTIONS_INVALID"
	CodeTypeMismatch       = "SEAL_TYPE_MISMATCH"
	CodeDocumentInvalid    = "SEAL_DOCUMENT_INVALID"
	CodeSealFailed         = "SEAL_FAILED"
)
