// scenarios.go is the throwaway shell: guided walkthroughs over envelope.go.
// Each scenario is a description plus ordered steps; after EVERY step the full
// relevant state is captured for report.go to render.
package main

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Sample event (the producer's declaration; shared verbatim by the consumer)
// ---------------------------------------------------------------------------

// Card is the subject subtree. PAN below is the PUBLISHED TEST VECTOR 4111111111111111.
// No CVV: SAD is barred from the outbox lane (#1304) and from this prototype entirely.
type Card struct {
	PAN    string `json:"pan"`
	Holder string `json:"holder"`
}

// PaymentAuthorized is the sample sealed event: two-kid identity on the sentinel,
// one subject. The subject's json name ("card") IS the sp manifest entry.
type PaymentAuthorized struct {
	_        struct{} `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	OrderID  string   `json:"orderId"`
	Amount   int64    `json:"amount"`
	Currency string   `json:"currency"`
	Card     *Card    `json:"card" seal:"subject"`
}

const (
	eventPaymentAuthorized = "payment.authorized"
	eventCardDeleted       = "card.deleted"
	famPaymentsSign        = "svc-payments-sign"
	famAcmeEnc             = "acme-core-enc"
)

func sampleEvent() PaymentAuthorized {
	return PaymentAuthorized{OrderID: "ord-1001", Amount: 1999, Currency: "USD", Card: &Card{PAN: "4111111111111111", Holder: "J DOE"}}
}

// ---------------------------------------------------------------------------
// Capture model
// ---------------------------------------------------------------------------

type KV struct{ Label, Value string }

type Step struct {
	Title  string
	Text   string
	State  []KV
	Failed bool // an opener rule fired (expected or not — the text says which)
}

type Scenario struct {
	ID          string
	Title       string
	Description string
	Steps       []Step
}

func (s *Scenario) step(title, text string) *Step {
	s.Steps = append(s.Steps, Step{Title: title, Text: text})
	return &s.Steps[len(s.Steps)-1]
}

func (st *Step) add(label string, v any) *Step {
	st.State = append(st.State, KV{Label: label, Value: render(v)})
	return st
}

func render(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return prettyJSON(x)
	case error:
		return x.Error()
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func prettyJSON(b []byte) string {
	var buf strings.Builder
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimRight(buf.String(), "\n")
}

// ---------------------------------------------------------------------------
// World: key material generated once at startup
// ---------------------------------------------------------------------------

type World struct {
	PaymentsSignV1, PaymentsSignV2 *rsa.PrivateKey
	AcmeEncV1                      *rsa.PrivateKey
	BillingSignV1                  *rsa.PrivateKey
	OtherAudienceEncV1             *rsa.PrivateKey
	MallorySignV1                  *rsa.PrivateKey
	FixedNow                       time.Time
}

func NewWorld() *World {
	return &World{
		PaymentsSignV1: GenerateKeyPair(), PaymentsSignV2: GenerateKeyPair(),
		AcmeEncV1: GenerateKeyPair(), BillingSignV1: GenerateKeyPair(),
		OtherAudienceEncV1: GenerateKeyPair(), MallorySignV1: GenerateKeyPair(),
		FixedNow: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
}

// producer = svc-payments with v1 active (sign private, acme-core public).
func (w *World) producer() *Producer {
	ks := NewKeystore().
		ProvisionPrivate("svc-payments-sign-v1", w.PaymentsSignV1).
		ProvisionPublic("acme-core-enc-v1", &w.AcmeEncV1.PublicKey)
	return &Producer{Keystore: ks, EventType: eventPaymentAuthorized, Now: func() time.Time { return w.FixedNow }}
}

// consumer = acme-core (sign public, enc private).
func (w *World) consumer() *Consumer {
	ks := NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("acme-core-enc-v1", w.AcmeEncV1)
	return &Consumer{Name: "acme-core", Keystore: ks, EventType: eventPaymentAuthorized}
}

// ---------------------------------------------------------------------------
// Shared step builders
// ---------------------------------------------------------------------------

func captureProducer(st *Step, p *Producer, evt any) {
	st.add("Producer struct (PAN = published test vector 4111111111111111)", evt)
	st.add("Producer keystore (accept set + roles; material never shown)", p.Keystore.Describe())
	st.add("Producer activation selector", p.Activation)
}

func captureFrame(st *Step, frame Frame, tr *SealTrace) {
	st.add("Wire body (compact JWS, exact bytes)", string(frame.Body))
	hdr, _ := sniffJWS(string(frame.Body))
	st.add("Outer JWS protected header (decoded)", hdr)
	st.add("Payload doc (signed bytes — subject replaced in place by the JWE)", JWSPayloadDoc(frame.Body))
	if tr != nil {
		st.add("Inner JWE protected header (decoded)", tr.JWEHeader)
	}
	st.add("AMQP headers map (unsigned)", frame.Headers)
}

func seal(st *Step, p *Producer, evt any) (Frame, *SealTrace) {
	frame, tr, err := p.Seal(evt)
	if err != nil {
		st.Failed = true
		st.add("Seal failed", err)
		return frame, tr
	}
	captureFrame(st, frame, tr)
	return frame, tr
}

// open runs the consumer + ledger and captures the verdict.
func open(st *Step, c *Consumer, frame Frame, ledger *Ledger) (*SealedEnvelope, *OpenError) {
	var got PaymentAuthorized
	st.add("Consumer", fmt.Sprintf("%s  EventType=%s  tenancy=%s  contextTenant=%q  acceptUnsealed=%v  familyPin=%v",
		c.Name, c.EventType, c.Tenancy, c.ContextTenant, c.AcceptUnsealed, !c.DisableFamilyPin))
	st.add("Consumer keystore (accept set + roles)", c.Keystore.Describe())
	env, tr, oerr := c.Open(frame, &got)
	if oerr != nil {
		st.Failed = true
		st.add("Open FAILED", fmt.Sprintf("rule %d fired → %s\n%s\ndisposition: %s", oerr.Rule, oerr.Code, oerr.Detail, oerr.Disposition))
		if tr != nil && tr.JWSHeader != nil {
			st.add("Outer header as seen before the failing rule", tr.JWSHeader)
		}
		if ledger != nil {
			st.add("Ledger", fmt.Sprintf("%s: untouched (nothing reaches the ledger before open succeeds)", ledger.Name))
		}
		return nil, oerr
	}
	if tr.Unsealed {
		st.add("Open result", DispositionPlaintextAccepted+"\n"+tr.Warn)
		st.add("Consumer's opened struct", got)
		st.add("SealedEnvelope", "nil — unsealed path exposes no envelope; dedup falls back to the header-sourced id")
		if ledger != nil {
			id := frame.Headers[HeaderOutboxEID]
			verdict, err := ledger.ProcessOnceHeaderID(id)
			if err != nil {
				st.add("Ledger ("+ledger.Name+") header-id path", err)
			} else {
				st.add("Ledger ("+ledger.Name+") header-id path", fmt.Sprintf("key %q → %s (dedup hits so far: %d)", id, verdict, ledger.DedupHits))
			}
		}
		return nil, nil
	}
	st.add("Open OK — consumer's opened struct", got)
	st.add("Opened payload doc (after splice at sp)", tr.OpenedDoc)
	st.add("SealedEnvelope (WithMeta door)", env)
	st.add("DedupKey()", env.DedupKey())
	if ledger != nil {
		verdict := ledger.ProcessOnce(env.DedupKey())
		st.add("Ledger ("+ledger.Name+") ProcessOnce", fmt.Sprintf("%s (dedup hits so far: %d)", verdict, ledger.DedupHits))
	}
	return env, nil
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

func AllScenarios(w *World) []*Scenario {
	return []*Scenario{
		s1Happy(w), s2Tamper(w), s3WrongKey(w), s4StripResign(w), s5Rotation(w),
		s6CrossType(w), s7Unsealed(w), s8Tenant(w), s9NilSubject(w), s10Sizes(w),
	}
}

func s1Happy(w *World) *Scenario {
	s := &Scenario{ID: "s1", Title: "S1 Happy path + redelivery",
		Description: "svc-payments seals PaymentAuthorized (sign family svc-payments-sign, encrypt family acme-core-enc); acme-core opens it; the same bytes are delivered again and the ledger short-circuits on the same DedupKey."}
	p, c, ledger := w.producer(), w.consumer(), NewLedger("acme-core inbox")
	evt := sampleEvent()

	st := s.step("1. Producer declares and seals", "Seal runs ONCE: encrypt the subject (card) to acme-core-enc-v1, splice the JWE in place, serialize once, sign the exact bytes as PS256 under svc-payments-sign-v1. jti and iat are minted here.")
	captureProducer(st, p, evt)
	spec, _ := ScanType(reflect.TypeOf(evt))
	st.add("Scanned SealSpec", spec)
	frame, _ := seal(st, p, evt)

	st = s.step("2. Consumer opens (first delivery)", "Rules 1–11 pass; the WithMeta door exposes SealedEnvelope; the ledger records DedupKey = <sign family>:<jti>.")
	open(st, c, frame, ledger)

	st = s.step("3. Redelivery — the SAME bytes again", "Broker redelivery is byte-identical. The seal layer opens it exactly as before (it judges bytes, never delivery history); the ledger sees the same DedupKey and skips.")
	open(st, c, frame, ledger)
	return s
}

func s2Tamper(w *World) *Scenario {
	s := &Scenario{ID: "s2", Title: "S2 Tampered clear field",
		Description: "An attacker flips the clear-text amount inside the base64url payload segment and keeps the original signature. Signed bytes are wire bytes, so rule 5 fires."}
	p, c := w.producer(), w.consumer()
	st := s.step("1. Seal", "Original message from S1's producer.")
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Attacker edits amount 1999 → 1, keeps header + signature", "Only the middle segment changes.")
	tampered := TamperPayload(frame.Body, func(doc map[string]json.RawMessage) { doc["amount"] = json.RawMessage("1") })
	captureFrame(st, Frame{Body: tampered, Headers: frame.Headers}, nil)

	st = s.step("3. Consumer opens", "Expected: rule 5 SEAL_SIGNATURE_INVALID, poison.")
	open(st, c, Frame{Body: tampered, Headers: frame.Headers}, NewLedger("acme-core inbox"))
	return s
}

func s3WrongKey(w *World) *Scenario {
	s := &Scenario{ID: "s3", Title: "S3 Wrong key",
		Description: "(a) a consumer whose acme-core-enc-v1 entry holds a DIFFERENT audience's private key → rule 9, sub-check decrypt; (a') a consumer whose keystore has no acme-core-enc generation at all → rule 9, unknown generation; (b) a message signed by another family (svc-billing-sign-v1) → rule 3."}
	p := w.producer()
	st := s.step("1. Seal", "Sealed for acme-core.")
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2a. Consumer holds another audience's private under the acme-core-enc-v1 name", "Family and generation names match, so rules 1–8 pass and rule 9 reaches the decrypt sub-check.")
	wrong := &Consumer{Name: "impostor-audience", EventType: eventPaymentAuthorized, Keystore: NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("acme-core-enc-v1", w.OtherAudienceEncV1)}
	open(st, wrong, frame, nil)

	st = s.step("2a'. Consumer has no acme-core-enc private at all (only other-audience-enc-v1)", "The inner kid is still a generation of the declared family, so it is an UNKNOWN GENERATION on this consumer — recoverable code, same DLQ path.")
	noEnc := &Consumer{Name: "other-audience", EventType: eventPaymentAuthorized, Keystore: NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("other-audience-enc-v1", w.OtherAudienceEncV1)}
	open(st, noEnc, frame, nil)

	st = s.step("3. Message signed by svc-billing-sign-v1 (another family)", "The consumer even holds billing's public key (it consumes billing events too) — the family pin is what refuses it, not key resolution.")
	// A billing producer cannot even seal this type (its sentinel names svc-payments-sign), so the
	// wire form is built by re-signing Alice's payload doc under billing's key.
	forged := ResignPayload(JWSPayloadDoc(frame.Body), mustHeader(frame.Body), "svc-billing-sign-v1", w.BillingSignV1)
	captureFrame(st, Frame{Body: forged, Headers: frame.Headers}, nil)
	c := w.consumer()
	c.Keystore.ProvisionPublic("svc-billing-sign-v1", &w.BillingSignV1.PublicKey)
	open(st, c, Frame{Body: forged, Headers: frame.Headers}, nil)
	return s
}

func s4StripResign(w *World) *Scenario {
	s := &Scenario{ID: "s4", Title: "S4 Strip-and-re-sign",
		Description: "Mallory (a legitimate producer of some other event, so acme-core holds her public key) keeps Alice's payload doc — including Alice's inner JWE — and re-signs it under mallory-sign-v1. Rule 3 kills it. With the family pin deliberately disabled (prototype-only knob), rule 9's iss≠kid authorship binding still kills it."}
	p, c := w.producer(), w.consumer()
	c.Keystore.ProvisionPublic("mallory-sign-v1", &w.MallorySignV1.PublicKey)

	st := s.step("1. Alice seals", "Inner JWE carries iss = svc-payments-sign-v1, AEAD-bound.")
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Mallory strips the signature and re-signs the same payload doc", "Outer kid becomes mallory-sign-v1; payload doc byte-identical (inner JWE untouched, iss still says Alice).")
	forged := ResignPayload(JWSPayloadDoc(frame.Body), mustHeader(frame.Body), "mallory-sign-v1", w.MallorySignV1)
	captureFrame(st, Frame{Body: forged, Headers: frame.Headers}, nil)

	st = s.step("3. Consumer opens (family pin ON)", "Expected: rule 3 SEAL_KID_FAMILY_MISMATCH.")
	open(st, c, Frame{Body: forged, Headers: frame.Headers}, nil)

	st = s.step("4. Defense in depth: family pin OFF (debug knob)", "Rules 3 skipped, 4 resolves mallory-sign-v1 public, 5 verifies (Mallory really signed it), 6–8 pass. Expected: rule 9 SEAL_AUTHORSHIP_MISMATCH — the JWE's iss says svc-payments-sign-v1, the outer kid says mallory-sign-v1.")
	c.DisableFamilyPin = true
	open(st, c, Frame{Body: forged, Headers: frame.Headers}, nil)
	return s
}

func s5Rotation(w *World) *Scenario {
	s := &Scenario{ID: "s5", Title: "S5 Rotation overlap",
		Description: "Provision svc-payments-sign-v2 to both sides, activate v2 on the producer. A consumer holding v1+v2 opens a v2 message and an old v1 message; the wire kid names the concrete generation each time. A consumer lacking v2 hits rule 4 (recoverable). Two generations provisioned with no activation is a startup error."}
	p := w.producer()
	st := s.step("1. Old traffic under v1 (before rotation)", "Baseline; this frame stays in flight (outbox replay) across the rotation.")
	oldFrame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Provision v2 private on the producer, no activation yet", "Two generations, no selector: startup refuses to guess.")
	p.Keystore.ProvisionPrivate("svc-payments-sign-v2", w.PaymentsSignV2)
	st.add("Producer keystore", p.Keystore.Describe())
	if _, _, err := p.Startup(mustSpec()); err != nil {
		st.Failed = true
		st.add("Startup", err)
	}

	st = s.step("3. Activate v2 on the producer; seal new traffic", "Activation is the deliberate, reviewable act. Wire kid now svc-payments-sign-v2.")
	p.Activation = Activation{famPaymentsSign: "v2"}
	captureProducer(st, p, sampleEvent())
	newFrame, _ := seal(st, p, sampleEvent())

	st = s.step("4. Consumer holding v1+v2 opens the v2 message", "Accept set widened by provisioning v2 public.")
	c := w.consumer()
	c.Keystore.ProvisionPublic("svc-payments-sign-v2", &w.PaymentsSignV2.PublicKey)
	ledger := NewLedger("acme-core inbox")
	open(st, c, newFrame, ledger)

	st = s.step("5. Same consumer opens the OLD v1 message (outbox replay during overlap)", "Per-message generation identity: v1 still resolves; no trial-verify.")
	open(st, c, oldFrame, ledger)

	st = s.step("6. Laggard consumer (v1 only) receives the v2 message", "Expected: rule 4 SEAL_KID_UNKNOWN_GENERATION — family matches, entry missing: the provisioning-gap signature, distinct from tampering.")
	laggard := w.consumer()
	laggard.Name = "acme-core (laggard replica)"
	open(st, laggard, newFrame, nil)

	st = s.step("7. Activation names an unprovisioned generation", "Selector says v3; nothing provisioned: startup error.")
	p.Activation = Activation{famPaymentsSign: "v3"}
	if _, _, err := p.Startup(mustSpec()); err != nil {
		st.Failed = true
		st.add("Startup", err)
	}
	return s
}

func s6CrossType(w *World) *Scenario {
	s := &Scenario{ID: "s6", Title: "S6 Cross-type reroute",
		Description: "S1's bytes are delivered to a consumer declared EventType card.deleted that decodes the same struct and holds the same keys. Rule 6 (etyp) refuses it."}
	p := w.producer()
	st := s.step("1. Seal payment.authorized", "etyp = payment.authorized is inside the signed header.")
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Deliver to the card.deleted consumer", "This consumer's ledger has NEVER seen this jti, so a ledger cannot help: only rule 6 stands between the attacker and a second effect under a different handler. Expected: rule 6 SEAL_EVENT_TYPE_MISMATCH.")
	c := w.consumer()
	c.Name, c.EventType = "acme-core card.deleted consumer", eventCardDeleted
	open(st, c, frame, NewLedger("card.deleted inbox (empty)"))
	return s
}

func s7Unsealed(w *World) *Scenario {
	s := &Scenario{ID: "s7", Title: "S7 Unsealed body at a sealed consumer",
		Description: "Plaintext JSON at a sealed consumer is refused (NOT_SEALED). With AcceptUnsealed it is accepted with a WARN and dedups by the header-sourced id, which must pass the header-id grammar. A JWS-shaped body with a broken signature under AcceptUnsealed is still POISON — never plaintext fallback."}
	p, c := w.producer(), w.consumer()
	plain, _ := json.Marshal(sampleEvent())
	plainFrame := Frame{Body: plain, Headers: map[string]string{HeaderOutboxEID: "evt-7f3a"}}

	st := s.step("1. Plaintext JSON body, AcceptUnsealed=false", "Expected: rule 1 NOT_SEALED.")
	captureFrameRaw(st, plainFrame)
	open(st, c, plainFrame, NewLedger("acme-core inbox"))

	st = s.step("2. Same body, AcceptUnsealed=true", "Accepted with WARN; authenticity is OFF for this consumer. Dedup uses x-outbox-event-id through the header-id grammar.")
	c.AcceptUnsealed = true
	ledger := NewLedger("acme-core inbox")
	open(st, c, plainFrame, ledger)

	st = s.step("3. Header-sourced id spelled like a sealed dedup key", "x-outbox-event-id = \"svc-payments-sign:<uuid>\" — an attacker on an unsealed sibling queue tries to pre-insert a sealed key. The ':' is outside the header-id grammar, so it never enters the ledger.")
	evil := Frame{Body: plain, Headers: map[string]string{HeaderOutboxEID: famPaymentsSign + ":0f1c2a3b-0000-4000-8000-000000000000"}}
	open(st, c, evil, ledger)

	st = s.step("4. JWS-shaped body with a broken signature, AcceptUnsealed=true", "Expected: rule 5 SEAL_SIGNATURE_INVALID, poison — the knob never admits a sealed-shaped body that failed to open.")
	frame, _, _ := p.Seal(sampleEvent())
	tampered := TamperPayload(frame.Body, func(doc map[string]json.RawMessage) { doc["amount"] = json.RawMessage("1") })
	open(st, c, Frame{Body: tampered, Headers: frame.Headers}, ledger)

	st = s.step("5. JWS-shaped body with the wrong typ, AcceptUnsealed=true", "A JWS that is not the v1 sealed typ is NOT_SEALED but JWS-shaped → poison, not plaintext.")
	wrongTyp := ResignPayloadWithTyp(JWSPayloadDoc(frame.Body), mustHeader(frame.Body), "JWT", "svc-payments-sign-v1", w.PaymentsSignV1)
	open(st, c, Frame{Body: wrongTyp, Headers: frame.Headers}, ledger)
	return s
}

func s8Tenant(w *World) *Scenario {
	s := &Scenario{ID: "s8", Title: "S8 Tenant",
		Description: "The producer mirrors the ADR-087 stamp into the signed tid. Shared-tenancy consumer: tid must equal the x-tenant-id carrier. Per-tenant consumer: tid present-and-different from the context tenant is poison; absent is accepted."}
	p := w.producer()
	p.Tenant = "tenant-a"
	st := s.step("1. Seal with tenant-a resolved", "tid = tenant-a in the signed header; x-tenant-id = tenant-a as the unsigned carrier.")
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Shared-tenancy consumer, carrier rewritten to tenant-b", "A publish-ACL holder rewrites the unsigned x-tenant-id header. Expected: rule 7 SEAL_TENANT_MISMATCH.")
	c := w.consumer()
	c.Tenancy = TenancyShared
	rewritten := Frame{Body: frame.Body, Headers: map[string]string{HeaderSealed: HeaderSealedV1, HeaderTenantID: "tenant-b"}}
	captureFrameRaw(st, rewritten)
	open(st, c, rewritten, nil)

	st = s.step("3. Shared-tenancy consumer, carrier intact", "Accepted.")
	open(st, c, frame, NewLedger("shared inbox"))

	st = s.step("4. Per-tenant consumer on tenant-b's vhost receives the tenant-a message", "Captured on A, re-published on B. Expected: rule 7 SEAL_TENANT_MISMATCH.")
	pt := w.consumer()
	pt.Tenancy, pt.ContextTenant = TenancyPerTenant, "tenant-b"
	open(st, pt, frame, nil)

	st = s.step("5. Per-tenant consumer, tid absent", "Producer sealed with no tenant resolved. Accepted.")
	p.Tenant = ""
	noTid, _ := seal(st, p, sampleEvent())
	open(st, pt, noTid, NewLedger("tenant-b inbox"))

	st = s.step("6. Shared-tenancy consumer, tid absent AND carrier absent", "Both empty: equal, accepted — a shared consumer cannot tell 'unstamped' from 'no tenant' (see DX findings).")
	open(st, c, noTid, nil)
	return s
}

func s9NilSubject(w *World) *Scenario {
	s := &Scenario{ID: "s9", Title: "S9 Nil subject",
		Description: "Card is nil: the subject is still sealed (a JWE of the JSON literal null), sp is unchanged, and the consumer opens to a nil Card. One wire shape per event type."}
	p, c := w.producer(), w.consumer()
	evt := sampleEvent()
	evt.Card = nil
	st := s.step("1. Seal with Card = nil", "The payload doc shows \"card\":\"<JWE>\" exactly as in S1.")
	captureProducer(st, p, evt)
	frame, _ := seal(st, p, evt)
	st = s.step("2. Open", "Opened struct has card = null; envelope identical in shape.")
	open(st, c, frame, NewLedger("acme-core inbox"))
	return s
}

func s10Sizes(w *World) *Scenario {
	s := &Scenario{ID: "s10", Title: "S10 Sizes (informational)",
		Description: "Plaintext bytes vs wire bytes vs the inner JWE alone, and the overhead ratio. RSA-OAEP-256 wraps a 256-bit CEK (256 B encrypted key) and PS256 adds a 256 B signature; both base64url-expanded."}
	p := w.producer()
	st := s.step("1. Seal S1's event", "")
	frame, tr, _ := p.Seal(sampleEvent())
	st.add("Sizes", MeasureSizes(tr, frame))
	evt := sampleEvent()
	evt.Card = nil
	frame2, tr2, _ := p.Seal(evt)
	st.add("Sizes with nil subject", MeasureSizes(tr2, frame2))
	return s
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func mustSpec() *SealSpec {
	spec, err := ScanType(reflect.TypeOf(PaymentAuthorized{}))
	if err != nil {
		panic(err)
	}
	return spec
}

func mustHeader(body []byte) map[string]any {
	h, _ := sniffJWS(string(body))
	return h
}

func captureFrameRaw(st *Step, frame Frame) {
	st.add("Wire body", string(frame.Body))
	st.add("AMQP headers map (unsigned)", frame.Headers)
}
