// scenarios.go is the throwaway shell: guided walkthroughs over envelope.go.
// Each scenario is a description plus ordered steps; every step declares what it
// EXPECTS (a code, a startup error, or a clean open) and records what FIRED, so the
// report self-asserts. After every step the full relevant state is captured.
package main

import (
	"context"
	"crypto/rsa"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

//go:embed module_shape.go
var moduleShapeSource string

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
	codeNone               = Code("") // expected/fired: clean open or clean seal
)

func sampleEvent() PaymentAuthorized {
	return PaymentAuthorized{OrderID: "ord-1001", Amount: 1999, Currency: "USD", Card: &Card{PAN: "4111111111111111", Holder: "J DOE"}}
}

// ---------------------------------------------------------------------------
// Capture model
// ---------------------------------------------------------------------------

type KV struct{ Label, Value string }

// SideBySide is the producer-wrote / travelled / consumer-saw view of one open.
type SideBySide struct{ Wrote, Travelled, Saw string }

type Step struct {
	ID          string
	Title       string
	Text        string
	Expect      Code // codeNone = expect a clean open/seal; CodeStartupError = expect a startup error
	Fired       Code
	Disposition string
	State       []KV
	Compare     *SideBySide
}

func (st *Step) OK() bool { return st.Expect == st.Fired }

type Scenario struct {
	ID          string
	Title       string
	Description string
	Steps       []Step
	lastActor   string
}

func (s *Scenario) step(title, text string, expect Code) *Step {
	s.Steps = append(s.Steps, Step{ID: fmt.Sprintf("%s-%d", s.ID, len(s.Steps)+1), Title: title, Text: text, Expect: expect})
	return &s.Steps[len(s.Steps)-1]
}

func (st *Step) add(label string, v any) *Step {
	st.State = append(st.State, KV{Label: label, Value: render(v)})
	return st
}

func (st *Step) fire(code Code, disposition string) {
	st.Fired, st.Disposition = code, disposition
}

func render(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return prettyJSON(x)
	case error:
		return x.Error()
	case nil:
		return "(none)"
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
	AcmeEncV1, AcmeEncV2           *rsa.PrivateKey
	BillingSignV1                  *rsa.PrivateKey
	OtherAudienceEncV1             *rsa.PrivateKey
	MallorySignV1                  *rsa.PrivateKey
	FixedNow                       time.Time
}

func NewWorld() *World {
	return &World{
		PaymentsSignV1: GenerateKeyPair(), PaymentsSignV2: GenerateKeyPair(),
		AcmeEncV1: GenerateKeyPair(), AcmeEncV2: GenerateKeyPair(), BillingSignV1: GenerateKeyPair(),
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

// consumer = acme-core (sign public, enc private), started.
func (w *World) consumer() *Consumer {
	ks := NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("acme-core-enc-v1", w.AcmeEncV1)
	return mustStart(&Consumer{Name: "acme-core", Keystore: ks, EventType: eventPaymentAuthorized})
}

func mustStart(c *Consumer) *Consumer {
	if err := c.Startup(reflect.TypeOf(PaymentAuthorized{})); err != nil {
		panic(err)
	}
	return c
}

// ---------------------------------------------------------------------------
// Shared step builders
// ---------------------------------------------------------------------------

func captureProducer(st *Step, p *Producer, evt any) {
	st.add("Producer struct (PAN = published test vector 4111111111111111)", evt)
	st.add("Producer keystore (accept set + roles; material never shown)", p.Keystore.Describe())
	if p.Activation == nil {
		st.add("Producer activation selector", "(none — a single provisioned generation per family auto-activates)")
	} else {
		st.add("Producer activation selector", p.Activation)
	}
	if signKid, encKid, err := p.Startup(mustSpec()); err == nil {
		st.add("Resolved active generations", fmt.Sprintf("sign=%s  encrypt=%s", signKid, encKid))
	}
}

func captureFrame(st *Step, frame Frame) {
	parts := strings.SplitN(string(frame.Body), ".", 3)
	st.add("Wire body (compact JWS, exact bytes)", string(frame.Body))
	if len(parts) == 3 {
		st.add("Wire body split on '.' (header · payload · signature)", fmt.Sprintf("header:    %s\npayload:   %s\nsignature: %s", parts[0], parts[1], parts[2]))
	}
	if hdr, ok := sniffJWS(string(frame.Body)); ok {
		st.add("Outer JWS protected header (wire decode)", hdr)
	}
	raw := JWSPayloadDoc(frame.Body)
	st.add("Decoded payload, pretty-printed (subject replaced in place by the JWE)", raw)
	st.add("Decoded payload, exact raw bytes", string(raw))
	if jweHdr := wireInnerHeader(raw); jweHdr != nil {
		st.add("Inner JWE protected header (wire decode of the JWE inside the payload)", jweHdr)
	}
	st.add("AMQP delivery (unsigned): Type · ContentType · headers", fmt.Sprintf("Type=%q  ContentType=%q\n%s", frame.Type, frame.ContentType, render(frame.Headers)))
}

// wireInnerHeader decodes the inner JWE's protected header FROM THE WIRE payload doc.
func wireInnerHeader(payload []byte) map[string]any {
	var doc map[string]json.RawMessage
	if json.Unmarshal(payload, &doc) != nil {
		return nil
	}
	var compact string
	if json.Unmarshal(doc[mustSpec().SubjectPath], &compact) != nil {
		return nil
	}
	h, _ := decodeSegmentHeader(compact, 5)
	return h
}

func seal(st *Step, p *Producer, evt any) (Frame, *SealTrace) {
	frame, tr, err := p.Seal(evt)
	if err != nil {
		st.fire(CodeStartupError, "startup")
		st.add("Seal failed (startup)", err)
		return frame, tr
	}
	captureFrame(st, frame)
	return frame, tr
}

// open runs the consumer + ledger, captures the verdict and records what fired.
// want is the struct the producer wrote (for the round-trip check); nil skips it.
func open(s *Scenario, st *Step, c *Consumer, frame Frame, ledger *Ledger, want any) *Meta {
	var got PaymentAuthorized
	actor := fmt.Sprintf("%s  EventType=%s  tenancy=%s  contextTenant=%q  acceptUnsealed=%v  familyPin=%v\nkeystore: %s",
		c.Name, c.EventType, c.Tenancy, c.ContextTenant, c.AcceptUnsealed, !c.DisableFamilyPin, render(c.Keystore.Describe()))
	if actor != s.lastActor {
		st.add("Consumer (identity + accept set)", actor)
		s.lastActor = actor
	}
	meta, tr, oerr := c.Open(frame, &got)
	if oerr != nil {
		st.fire(oerr.Code, oerr.Disposition)
		st.add("Open FAILED", fmt.Sprintf("rule %d fired → %s\n%s\ndisposition: %s", oerr.Rule, oerr.Code, oerr.Detail, oerr.Disposition))
		if ledger != nil {
			st.add("Ledger", fmt.Sprintf("%s: untouched (nothing reaches the ledger before open succeeds)", ledger.Name))
		}
		return nil
	}
	if want != nil {
		st.Compare = &SideBySide{Wrote: render(want), Travelled: string(JWSPayloadDoc(frame.Body)), Saw: render(got)}
		if tr.Unsealed {
			st.Compare.Travelled = string(frame.Body)
		}
		st.add("Round-trip equal (reflect.DeepEqual producer vs opened)", fmt.Sprint(reflect.DeepEqual(want, got)))
	}
	if tr.Unsealed {
		st.fire(codeNone, DispositionPlaintextAccepted)
		st.add("Open result", DispositionPlaintextAccepted+"\n"+tr.Warn)
		_, sealed := meta.Sealed()
		st.add("meta.Sealed()", fmt.Sprintf("ok=%v — unsealed path exposes no envelope", sealed))
		key, err := meta.DedupKey()
		if err != nil {
			st.fire(CodeHeaderIDInvalid, "handler error — no dedup key; message nacked")
			st.add("meta.DedupKey()", err)
			return meta
		}
		st.add("meta.DedupKey() (header-sourced, grammar-validated)", key)
		if ledger != nil {
			st.add("Ledger ("+ledger.Name+") ProcessOnce", fmt.Sprintf("%s (dedup hits so far: %d)", ledger.ProcessOnce(key), ledger.DedupHits))
		}
		return meta
	}
	st.fire(codeNone, "opened")
	env, _ := meta.Sealed()
	st.add("SealedEnvelope via meta.Sealed()", env)
	key, _ := meta.DedupKey()
	st.add("meta.DedupKey()", key)
	if ledger != nil {
		st.add("Ledger ("+ledger.Name+") ProcessOnce", fmt.Sprintf("%s (dedup hits so far: %d)", ledger.ProcessOnce(key), ledger.DedupHits))
	}
	return meta
}

// forged captures a negative vector and asserts it differs from the positive header in
// exactly the intended fields.
func forged(st *Step, positive map[string]any, frame Frame, wantDiff ...string) {
	captureFrame(st, frame)
	hdr, _ := sniffJWS(string(frame.Body))
	diff := HeaderDiff(positive, hdr)
	verdict := "OK"
	if !equalStrings(diff, wantDiff) {
		verdict = "UNEXPECTED — vector differs in more than the intended field"
		st.fire(Code("VECTOR_NOT_MINIMAL"), verdict)
	}
	st.add("Negative vector differs from the positive header in", fmt.Sprintf("%v (intended %v) → %s", diff, wantDiff, verdict))
}

func startup(st *Step, err error) {
	if err != nil {
		st.fire(CodeStartupError, "startup")
		st.add("Startup", err)
	} else {
		st.add("Startup", "ok")
	}
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

func AllScenarios(w *World) []*Scenario {
	return []*Scenario{
		s0ModuleShape(w), s1Happy(w), s2Tamper(w), s3WrongKey(w), s4StripResign(w), s5Rotation(w),
		s6CrossType(w), s7Unsealed(w), s8Tenant(w), s9NilSubject(w), s10Sizes(w), s11Slots(w),
	}
}

func s0ModuleShape(w *World) *Scenario {
	s := &Scenario{ID: "s0", Title: "S0 Module-author code shape",
		Description: "The code a module author writes under #1305: DeclareMessaging with DeclareTypedPublisher[PaymentAuthorized], an h.Publish call site, DeclareTypedConsumerWithMeta with AcceptUnsealed, and a handler with ONE meta.DedupKey() call. The block below is module_shape.go verbatim and is what the following step executes."}
	st := s.step("1. The code (module_shape.go, verbatim)", "Framework stand-ins on top, module author's code at the bottom.", codeNone)
	st.add("module_shape.go", moduleShapeSource)

	st = s.step("2. Run it: DeclareMessaging → AuthorizePayment → consumer handler, twice", "Publish once, deliver twice (redelivery). The handler's single DedupKey call yields processed then duplicate.", codeNone)
	d := &Declarations{producer: w.producer(), consumer: w.consumer()}
	m := &paymentsModule{ledger: NewLedger("acme-core inbox")}
	m.DeclareMessaging(d)
	client := &Client{}
	if err := m.AuthorizePayment(context.Background(), client, sampleEvent()); err != nil {
		st.fire(CodeStartupError, "publish failed")
		st.add("Publish", err)
		return s
	}
	frame := client.Published[0]
	captureFrame(st, frame)
	for i := 0; i < 2; i++ {
		if err := d.consumers[0].handler(context.Background(), frame); err != nil {
			st.fire(Code(fmt.Sprint(err)), "handler error")
		}
	}
	st.add("Handler log", strings.Join(m.log, "\n"))
	st.add("Ledger dedup hits", fmt.Sprint(m.ledger.DedupHits))
	return s
}

func s1Happy(w *World) *Scenario {
	s := &Scenario{ID: "s1", Title: "S1 Happy path + redelivery",
		Description: "svc-payments seals PaymentAuthorized (sign family svc-payments-sign, encrypt family acme-core-enc); acme-core opens it; the same bytes are delivered again and the ledger short-circuits on the same DedupKey."}
	p, c, ledger := w.producer(), w.consumer(), NewLedger("acme-core inbox")
	evt := sampleEvent()

	st := s.step("1. Producer declares and seals", "Seal runs ONCE: encrypt the subject (card) to acme-core-enc-v1, splice the JWE in place, serialize once, sign the exact bytes as PS256 under svc-payments-sign-v1. jti and iat are minted here.", codeNone)
	captureProducer(st, p, evt)
	st.add("Scanned SealSpec (consumer side, cached at Startup)", c.Spec())
	frame, _ := seal(st, p, evt)

	st = s.step("2. Consumer opens (first delivery)", "Rules 1–12 pass; the WithMeta door exposes SealedEnvelope; the ledger records DedupKey = <sign family>:<jti>.", codeNone)
	open(s, st, c, frame, ledger, evt)

	st = s.step("3. Redelivery — the SAME bytes again", "Broker redelivery is byte-identical. The seal layer opens it exactly as before (it judges bytes, never delivery history); the ledger sees the same DedupKey and skips.", codeNone)
	open(s, st, c, frame, ledger, evt)
	return s
}

func s2Tamper(w *World) *Scenario {
	s := &Scenario{ID: "s2", Title: "S2 Tampered clear field",
		Description: "An attacker flips the clear-text amount inside the base64url payload segment and keeps the original signature. Signed bytes are wire bytes, so rule 5 fires."}
	p, c := w.producer(), w.consumer()
	st := s.step("1. Seal", "Original message from S1's producer.", codeNone)
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Attacker edits amount 1999 → 1, keeps header + signature", "Only the middle segment changes.", codeNone)
	tampered := frame
	tampered.Body = TamperPayload(frame.Body, func(doc map[string]json.RawMessage) { doc["amount"] = json.RawMessage("1") })
	captureFrame(st, tampered)

	st = s.step("3. Consumer opens", "Expected: rule 5 SEAL_SIGNATURE_INVALID, poison.", CodeSignatureInvalid)
	open(s, st, c, tampered, NewLedger("acme-core inbox"), nil)
	return s
}

func s3WrongKey(w *World) *Scenario {
	s := &Scenario{ID: "s3", Title: "S3 Wrong key",
		Description: "(a) a consumer whose acme-core-enc-v1 entry holds a DIFFERENT audience's private key → rule 10, sub-check decrypt; (a') the wire names acme-core-enc-v2 and the consumer holds only v1 → rule 10, unknown generation (recoverable); a consumer with NO encrypt-family private at all cannot boot (startup); (b) a message signed by another family (svc-billing-sign-v1) → rule 3."}
	p := w.producer()
	st := s.step("1. Seal", "Sealed for acme-core under acme-core-enc-v1.", codeNone)
	frame, tr := seal(st, p, sampleEvent())

	st = s.step("2a. Consumer holds another audience's private under the acme-core-enc-v1 name", "Family and generation names match, so rules 1–9 pass and rule 10 reaches the decrypt sub-check. Ops note: 'same name, different material' is a provisioning bug that looks like an attack.", CodeDecryptFailed)
	wrong := mustStart(&Consumer{Name: "impostor-audience", EventType: eventPaymentAuthorized, Keystore: NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("acme-core-enc-v1", w.OtherAudienceEncV1)})
	open(s, st, wrong, frame, nil, nil)

	st = s.step("2a'. Wire names acme-core-enc-v2; consumer holds only acme-core-enc-v1", "The producer activated enc v2 (its selector governs encrypt families too). The inner kid is a generation of the declared family the consumer has not provisioned: UNKNOWN GENERATION, recoverable.", CodeKidUnknownGen)
	p2 := w.producer()
	p2.Keystore.ProvisionPublic("acme-core-enc-v2", &w.AcmeEncV2.PublicKey)
	p2.Activation = Activation{famAcmeEnc: "v2"}
	captureProducer(st, p2, sampleEvent())
	frameV2, _ := seal(st, p2, sampleEvent())
	open(s, st, w.consumer(), frameV2, nil, nil)

	st = s.step("2a''. Consumer with no acme-core-enc private at all cannot boot", "#1306 startup taxonomy: the consumer must resolve at least one encrypt-family generation as PRIVATE. Expected: startup error (never per-message poison).", CodeStartupError)
	noEnc := &Consumer{Name: "other-audience", EventType: eventPaymentAuthorized, Keystore: NewKeystore().
		ProvisionPublic("svc-payments-sign-v1", &w.PaymentsSignV1.PublicKey).
		ProvisionPrivate("other-audience-enc-v1", w.OtherAudienceEncV1)}
	startup(st, noEnc.Startup(reflect.TypeOf(PaymentAuthorized{})))

	st = s.step("3. Message signed by svc-billing-sign-v1 (another family)", "A billing producer cannot even seal this type (the sentinel names svc-payments-sign), so the vector is Alice's payload doc re-signed under billing's key. The consumer holds billing's public key too (it consumes billing events) — the family pin refuses it, not key resolution.", CodeKidFamilyMismatch)
	billingFrame := frame
	billingFrame.Body = ResignPayload(JWSPayloadDoc(frame.Body), tr.JWSHeader, "svc-billing-sign-v1", w.BillingSignV1)
	forged(st, tr.JWSHeader, billingFrame, "kid")
	c := w.consumer()
	c.Keystore.ProvisionPublic("svc-billing-sign-v1", &w.BillingSignV1.PublicKey)
	open(s, st, c, billingFrame, nil, nil)
	return s
}

func s4StripResign(w *World) *Scenario {
	s := &Scenario{ID: "s4", Title: "S4 Strip-and-re-sign",
		Description: "Mallory (a legitimate producer of some other event, so acme-core holds her public key) keeps Alice's payload doc — including Alice's inner JWE — and re-signs it under mallory-sign-v1. Rule 3 kills it. With the family pin deliberately disabled (prototype-only knob), rule 10's iss≠kid authorship binding still kills it."}
	p, c := w.producer(), w.consumer()
	c.Keystore.ProvisionPublic("mallory-sign-v1", &w.MallorySignV1.PublicKey)

	st := s.step("1. Alice seals", "Inner JWE carries iss = svc-payments-sign-v1, AEAD-bound.", codeNone)
	frame, tr := seal(st, p, sampleEvent())

	st = s.step("2. Mallory strips the signature and re-signs the same payload doc", "Outer kid becomes mallory-sign-v1; payload doc byte-identical (inner JWE untouched, iss still says Alice).", codeNone)
	mal := frame
	mal.Body = ResignPayload(JWSPayloadDoc(frame.Body), tr.JWSHeader, "mallory-sign-v1", w.MallorySignV1)
	forged(st, tr.JWSHeader, mal, "kid")

	st = s.step("3. Consumer opens (family pin ON)", "Expected: rule 3 SEAL_KID_FAMILY_MISMATCH.", CodeKidFamilyMismatch)
	open(s, st, c, mal, nil, nil)

	st = s.step("4. Defense in depth: family pin OFF (debug knob)", "Rule 3 skipped, 4 resolves mallory-sign-v1 public, 5 verifies (Mallory really signed it), 6–9 pass. Expected: rule 10 SEAL_AUTHORSHIP_MISMATCH — the JWE's iss says svc-payments-sign-v1, the outer kid says mallory-sign-v1.", CodeAuthorshipMismatch)
	c.DisableFamilyPin = true
	open(s, st, c, mal, nil, nil)
	return s
}

func s5Rotation(w *World) *Scenario {
	s := &Scenario{ID: "s5", Title: "S5 Rotation overlap",
		Description: "Provision svc-payments-sign-v2 to both sides, activate v2 on the producer. A consumer holding v1+v2 opens a v2 message and an old v1 message; the wire kid names the concrete generation each time. A consumer lacking v2 hits rule 4 (recoverable). Two generations provisioned with no activation is a startup error."}
	p := w.producer()
	st := s.step("1. Old traffic under v1 (before rotation)", "Baseline; this frame stays in flight (outbox replay) across the rotation.", codeNone)
	captureProducer(st, p, sampleEvent())
	oldFrame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Provision v2 private on the producer, no activation yet", "Two generations, no selector: startup refuses to guess.", CodeStartupError)
	p.Keystore.ProvisionPrivate("svc-payments-sign-v2", w.PaymentsSignV2)
	st.add("Producer keystore", p.Keystore.Describe())
	_, _, err := p.Startup(mustSpec())
	startup(st, err)

	st = s.step("3. Activate v2 on the producer; seal new traffic", "Activation is the deliberate, reviewable act. Wire kid now svc-payments-sign-v2.", codeNone)
	p.Activation = Activation{famPaymentsSign: "v2"}
	captureProducer(st, p, sampleEvent())
	newFrame, _ := seal(st, p, sampleEvent())

	st = s.step("4. Consumer holding v1+v2 opens the v2 message", "Accept set widened by provisioning v2 public.", codeNone)
	c := w.consumer()
	c.Keystore.ProvisionPublic("svc-payments-sign-v2", &w.PaymentsSignV2.PublicKey)
	mustStart(c)
	ledger := NewLedger("acme-core inbox")
	open(s, st, c, newFrame, ledger, sampleEvent())

	st = s.step("5. Same consumer opens the OLD v1 message (outbox replay during overlap)", "Per-message generation identity: v1 still resolves; no trial-verify.", codeNone)
	open(s, st, c, oldFrame, ledger, sampleEvent())

	st = s.step("6. Laggard consumer (v1 only) receives the v2 message", "Expected: rule 4 SEAL_KID_UNKNOWN_GENERATION — family matches, entry missing: the provisioning-gap signature, distinct from tampering. Note rule 4 precedes verify, so this code is unauthenticated: any publish-ACL holder can raise it.", CodeKidUnknownGen)
	laggard := w.consumer()
	laggard.Name = "acme-core (laggard replica)"
	open(s, st, laggard, newFrame, nil, nil)

	st = s.step("7. Activation names an unprovisioned generation", "Selector says v3; nothing provisioned: startup error.", CodeStartupError)
	p.Activation = Activation{famPaymentsSign: "v3"}
	_, _, err = p.Startup(mustSpec())
	startup(st, err)
	return s
}

func s6CrossType(w *World) *Scenario {
	s := &Scenario{ID: "s6", Title: "S6 Cross-type reroute",
		Description: "S1's bytes are delivered to a consumer declared EventType card.deleted that decodes the same struct and holds the same keys. Rule 7 (etyp) refuses it."}
	p := w.producer()
	st := s.step("1. Seal payment.authorized", "etyp = payment.authorized is inside the signed header; delivery.Type is its unsigned, never-compared twin.", codeNone)
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Deliver to the card.deleted consumer", "This consumer's ledger has NEVER seen this jti, so a ledger cannot help: only rule 7 stands between the attacker and a second effect under a different handler. Expected: rule 7 SEAL_EVENT_TYPE_MISMATCH.", CodeEventTypeMismatch)
	c := w.consumer()
	c.Name, c.EventType = "acme-core card.deleted consumer", eventCardDeleted
	mustStart(c)
	open(s, st, c, frame, NewLedger("card.deleted inbox (empty)"), nil)
	return s
}

func s7Unsealed(w *World) *Scenario {
	s := &Scenario{ID: "s7", Title: "S7 Unsealed body at a sealed consumer",
		Description: "Plaintext JSON at a sealed consumer is refused (NOT_SEALED). With AcceptUnsealed it is accepted with a WARN and dedups by the header-sourced id through meta.DedupKey(), which grammar-validates it. Any JWS-shaped body (3 dot-segments, segment 0 a JSON object) takes the sealed branch — a broken signature or a wrong typ under AcceptUnsealed is still POISON, never plaintext fallback."}
	p, c := w.producer(), w.consumer()
	evt := sampleEvent()
	plain, _ := json.Marshal(evt)
	plainFrame := Frame{Body: plain, Type: eventPaymentAuthorized, ContentType: ContentTypeOctet, Headers: map[string]string{HeaderOutboxEID: "evt-7f3a"}}

	st := s.step("1. Plaintext JSON body, AcceptUnsealed=false", "Expected: rule 1 NOT_SEALED.", CodeNotSealed)
	captureFrame(st, plainFrame)
	open(s, st, c, plainFrame, NewLedger("acme-core inbox"), nil)

	st = s.step("2. Same body, AcceptUnsealed=true", "Accepted with WARN; authenticity is OFF for this consumer. meta.DedupKey() returns the x-outbox-event-id after the header-id grammar.", codeNone)
	c.AcceptUnsealed = true
	ledger := NewLedger("acme-core inbox")
	open(s, st, c, plainFrame, ledger, evt)

	st = s.step("3. Header-sourced id spelled like a sealed dedup key", "x-outbox-event-id = \"svc-payments-sign:<uuid>\" — an attacker on an unsealed sibling queue tries to pre-insert a sealed key. The ':' is outside the header-id grammar, so meta.DedupKey() errors and the key never enters the ledger.", CodeHeaderIDInvalid)
	evil := plainFrame
	evil.Headers = map[string]string{HeaderOutboxEID: famPaymentsSign + ":0f1c2a3b-0000-4000-8000-000000000000"}
	open(s, st, c, evil, ledger, nil)

	st = s.step("4. Unsealed body with NO x-outbox-event-id, AcceptUnsealed=true", "Opens, but meta.DedupKey() has nothing valid to return: error (the handler decides; the prototype nacks). #1309 must state the rule.", CodeHeaderIDInvalid)
	noID := plainFrame
	noID.Headers = map[string]string{}
	open(s, st, c, noID, ledger, nil)

	st = s.step("5. JWS-shaped body with a broken signature, AcceptUnsealed=true", "Expected: rule 5 SEAL_SIGNATURE_INVALID, poison — the knob never admits a sealed-shaped body that failed to open.", CodeSignatureInvalid)
	frame, tr, _ := p.Seal(sampleEvent())
	tampered := frame
	tampered.Body = TamperPayload(frame.Body, func(doc map[string]json.RawMessage) { doc["amount"] = json.RawMessage("1") })
	open(s, st, c, tampered, ledger, nil)

	st = s.step("6. JWS-shaped body whose signature segment is not even base64url, AcceptUnsealed=true", "Segment 0 is a JSON object, so the sniff takes the sealed branch by definition; rule 5's parse reports the malformed segment. Expected: SEAL_SIGNATURE_INVALID, never plaintext.", CodeSignatureInvalid)
	parts := strings.Split(string(frame.Body), ".")
	garbage := frame
	garbage.Body = []byte(parts[0] + "." + parts[1] + ".!!not-base64url!!")
	open(s, st, c, garbage, ledger, nil)

	st = s.step("7. JWS-shaped body with the wrong typ, AcceptUnsealed=true", "A JWS that is not the v1 sealed typ is NOT_SEALED but JWS-shaped → poison, not plaintext.", CodeNotSealed)
	wrongTyp := frame
	wrongTyp.Body = ForgeHeader(JWSPayloadDoc(frame.Body), tr.JWSHeader, w.PaymentsSignV1, func(h map[string]any) { h["typ"] = "JWT" })
	forged(st, tr.JWSHeader, wrongTyp, "typ")
	open(s, st, c, wrongTyp, ledger, nil)
	return s
}

func s8Tenant(w *World) *Scenario {
	s := &Scenario{ID: "s8", Title: "S8 Tenant",
		Description: "The producer mirrors the ADR-087 stamp into the signed tid. Shared-tenancy consumer: tid must be present (slot) and equal the x-tenant-id carrier the pipeline admitted. Per-tenant consumer: tid present-and-different from the context tenant is poison; absent is accepted."}
	p := w.producer()
	p.Tenant = "tenant-a"
	st := s.step("1. Seal with tenant-a resolved", "tid = tenant-a in the signed header; x-tenant-id = tenant-a as the unsigned carrier.", codeNone)
	frame, _ := seal(st, p, sampleEvent())

	st = s.step("2. Shared-tenancy consumer, carrier rewritten to tenant-b", "A publish-ACL holder rewrites the unsigned x-tenant-id header. Expected: rule 8 SEAL_TENANT_MISMATCH.", CodeTenantMismatch)
	c := w.consumer()
	c.Tenancy = TenancyShared
	rewritten := frame
	rewritten.Headers = map[string]string{HeaderSealed: HeaderSealedV1, HeaderTenantID: "tenant-b"}
	captureFrame(st, rewritten)
	open(s, st, c, rewritten, nil, nil)

	st = s.step("3. Shared-tenancy consumer, carrier intact", "Accepted.", codeNone)
	open(s, st, c, frame, NewLedger("shared inbox"), sampleEvent())

	st = s.step("4. Per-tenant consumer on tenant-b's vhost receives the tenant-a message", "Captured on A, re-published on B. Expected: rule 8 SEAL_TENANT_MISMATCH.", CodeTenantMismatch)
	pt := w.consumer()
	pt.Tenancy, pt.ContextTenant = TenancyPerTenant, "tenant-b"
	open(s, st, pt, frame, nil, nil)

	st = s.step("5. Per-tenant consumer, tid absent", "Producer sealed with no tenant resolved. Accepted.", codeNone)
	p.Tenant = ""
	noTid, _ := seal(st, p, sampleEvent())
	open(s, st, pt, noTid, NewLedger("tenant-b inbox"), sampleEvent())

	st = s.step("6. Shared-tenancy consumer, tid absent", "Carrier presence is the delivery pipeline's gate before open; a shared consumer only ever sees stamped deliveries, so a signed tid is REQUIRED there. Expected: rule 6 SEAL_HEADER_SLOT_INVALID (slot tid).", CodeHeaderSlotInvalid)
	open(s, st, c, noTid, nil, nil)
	return s
}

func s9NilSubject(w *World) *Scenario {
	s := &Scenario{ID: "s9", Title: "S9 Nil subject",
		Description: "Card is nil: the subject is still sealed (a JWE of the JSON literal null), sp is unchanged, and the consumer opens to a nil Card. One wire shape per event type."}
	p, c := w.producer(), w.consumer()
	evt := sampleEvent()
	evt.Card = nil
	st := s.step("1. Seal with Card = nil", "The payload doc shows \"card\":\"<JWE>\" exactly as in S1.", codeNone)
	captureProducer(st, p, evt)
	frame, _ := seal(st, p, evt)
	st = s.step("2. Open", "Opened struct has card = null; envelope identical in shape.", codeNone)
	open(s, st, c, frame, NewLedger("acme-core inbox"), evt)
	return s
}

func s10Sizes(w *World) *Scenario {
	s := &Scenario{ID: "s10", Title: "S10 Sizes (informational)",
		Description: "Plaintext bytes vs wire bytes vs the inner JWE alone, and the overhead ratio. RSA-OAEP-256 wraps a 256-bit CEK (256 B encrypted key) and PS256 adds a 256 B signature; both base64url-expanded."}
	p := w.producer()
	st := s.step("1. Seal S1's event", "", codeNone)
	frame, tr, _ := p.Seal(sampleEvent())
	st.add("Sizes", MeasureSizes(tr, frame))
	evt := sampleEvent()
	evt.Card = nil
	frame2, tr2, _ := p.Seal(evt)
	st.add("Sizes with nil subject", MeasureSizes(tr2, frame2))
	return s
}

func s11Slots(w *World) *Scenario {
	s := &Scenario{ID: "s11", Title: "S11 Mandatory slots (#1307)",
		Description: "Every slot is mandatory and validated right after signature verify (rule 6), before any decrypt. Each vector is Alice's payload doc re-signed under Alice's own key with exactly ONE header field changed, so the signature is valid and only the slot rule can be what fires."}
	p, c := w.producer(), w.consumer()
	st := s.step("1. Positive vector", "Alice's own message.", codeNone)
	frame, tr := seal(st, p, sampleEvent())
	open(s, st, c, frame, nil, sampleEvent())

	vectors := []struct {
		title, field string
		mutate       func(h map[string]any)
	}{
		{"2. iat absent", "iat", func(h map[string]any) { delete(h, "iat") }},
		{"3. iat is a string", "iat", func(h map[string]any) { h["iat"] = "yesterday" }},
		{"4. iat negative", "iat", func(h map[string]any) { h["iat"] = int64(-1) }},
		{"5. iat fractional", "iat", func(h map[string]any) { h["iat"] = 1788436800.5 }},
		{"6. jti absent", "jti", func(h map[string]any) { delete(h, "jti") }},
		{"7. jti outside the identifier grammar", "jti", func(h map[string]any) { h["jti"] = "has:colon" }},
		{"8. etyp absent", "etyp", func(h map[string]any) { delete(h, "etyp") }},
		{"9. etyp empty", "etyp", func(h map[string]any) { h["etyp"] = "" }},
		{"10. sp absent", "sp", func(h map[string]any) { delete(h, "sp") }},
		{"11. sp empty array", "sp", func(h map[string]any) { h["sp"] = []string{} }},
	}
	for _, v := range vectors {
		st = s.step(v.title, "Signature valid; expected rule 6 SEAL_HEADER_SLOT_INVALID with slot "+v.field+".", CodeHeaderSlotInvalid)
		f := frame
		f.Body = ForgeHeader(JWSPayloadDoc(frame.Body), tr.JWSHeader, w.PaymentsSignV1, v.mutate)
		forged(st, tr.JWSHeader, f, v.field)
		open(s, st, c, f, nil, nil)
	}

	st = s.step("12. sp names a different path (well-formed, wrong)", "Slot rule passes (well-formed); rule 9 SEAL_MANIFEST_MISMATCH fires.", CodeManifestMismatch)
	f := frame
	f.Body = ForgeHeader(JWSPayloadDoc(frame.Body), tr.JWSHeader, w.PaymentsSignV1, func(h map[string]any) { h["sp"] = []string{"holder"} })
	forged(st, tr.JWSHeader, f, "sp")
	open(s, st, c, f, nil, nil)
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
