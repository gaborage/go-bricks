package sealed_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	josesealed "github.com/gaborage/go-bricks/jose/sealed"
	"github.com/gaborage/go-bricks/messaging"
)

// TestSealTagNameMatchesJoseSealed pins the two spellings of the tag key: jose
// must not import messaging and the messaging probe must not import the codec, so
// the literal exists twice by design and this test is what keeps them one.
func TestSealTagNameMatchesJoseSealed(t *testing.T) {
	assert.Equal(t, josesealed.TagName, messaging.SealTagName)
}

type agreementSubject struct {
	Card string `json:"card" seal:"subject"`
}

type agreementSealed struct {
	_    struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID   string   `json:"id"`
	Card string   `json:"card" seal:"subject"`
}

type agreementPromotedEmbed struct {
	agreementSubject
	ID string `json:"id"`
}

type agreementTaggedEmbed struct {
	agreementSubject `json:"inner"`
}

type agreementNestedField struct {
	Inner agreementSubject `json:"inner"`
}

type agreementEmbeddedSubject struct {
	_                struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	agreementSubject `seal:"subject"`
}

// TestIsSealTaggedAgreesWithScanType runs the probe and the codec's scan over one
// fixture set. Wherever ScanType speaks — a spec or a refusal — the probe says true;
// where ScanType is silent because the tag sits on a named nested field or a tagged
// embed, the probe STILL says true, because DeclareTypedPublisher refuses that shape
// (misplaced tag) and the lane guards must fail closed on the same set. Only a type
// with no seal tag anywhere is false.
func TestIsSealTaggedAgreesWithScanType(t *testing.T) {
	cases := map[string]struct {
		t         reflect.Type
		wantProbe bool
		scanSpeak bool // ScanType returns a spec OR an error
	}{
		"own_field":        {reflect.TypeOf(agreementSealed{}), true, true},
		"own_field_ptr":    {reflect.TypeOf((*agreementSealed)(nil)), true, true},
		"promoted_embed":   {reflect.TypeOf(agreementPromotedEmbed{}), true, true},
		"embedded_subject": {reflect.TypeOf(agreementEmbeddedSubject{}), true, true},
		"tagged_embed":     {reflect.TypeOf(agreementTaggedEmbed{}), true, false},
		"nested_field":     {reflect.TypeOf(agreementNestedField{}), true, false},
		"plain":            {reflect.TypeOf(struct{ ID string }{}), false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.wantProbe, messaging.IsSealTagged(tc.t), "IsSealTagged")
			spec, err := josesealed.ScanType(tc.t)
			spoke := spec != nil || err != nil
			require.Equal(t, tc.scanSpeak, spoke, "ScanType spoke (spec=%v err=%v)", spec != nil, err)
			if spoke {
				assert.True(t, messaging.IsSealTagged(tc.t), "the probe must speak wherever ScanType does")
			}
		})
	}
}
