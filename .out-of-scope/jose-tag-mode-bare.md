# Bare-JWE Mode on the jose Struct Tag

`jose.SealModeBareJWE` is selectable on a `Policy`, and therefore from
`jose.Seal`/`jose.Open` and from `httpclient.Builder.WithJOSE`. It is **not**
selectable from a `jose:` struct tag, and the tag scanner has no `mode` key.

## Why this is out of scope

Visa MLE — the integration that motivated bare mode in the first place — is
*outbound*: go-bricks calls Visa. One inbound peer does now exist (#2142): its
request body carries a compact JWE as a JSON member, signed by a detached JWS
in a request header, and it expects bare-sealed replies. That peer fired the
original reopen trigger, and the answer was the `Policy`-level door served
imperatively, per ADR-150: a typed handler on an untagged route builds a
policy per request and calls `jose.Open` / `jose.Seal` (recipe: #2165). It
needed no tag key, and its wire shape would not fit the tagged server path
anyway. The Policy-level door still covers every caller that exists, which is
what ADR-107 says when it states that bare mode is a `Policy`-level door with
no `mode` tag key. Adding the tag key is still surface with no caller.

It is also surface that cuts the wrong way. A `jose:` tag is a one-line struct
annotation; making a *sender-unauthenticated* inbound route selectable from one
would remove the code-review seam that a Policy construction gives you. Bare
mode means there is no outer JWS and therefore no sender authentication at all
— the decision to accept such a body deserves to be visible in code, not in a
field tag.

Mechanically it is not a one-key change either. The tag parser seeds the
signature algorithm with the default unconditionally, and validates `enc`
through a mode-unaware gate. A `mode` key would make tag ordering significant:
`enc=A128GCM,mode=bare` and `mode=bare,enc=A128GCM` would behave differently
unless the parser is reworked into two passes — collect first, validate after.
That is a real refactor of the parser's contract, not a new case in a switch.

And even after all of it, a Visa-shaped peer still would not interoperate. The
server inbound gate requires `application/jose`, while Visa MLE sends
`application/json` carrying `{"encData": …}`. A server-side `BodyEnvelope`
seam would have to be designed and added as well — and designing it against a
hypothetical peer means guessing the envelope shape, the Content-Type
negotiation, and the authentication mechanism that has to substitute for the
missing JWS.

## Reopen trigger

The earlier trigger, a concrete partner that pushes bare JWE bodies *to* a
go-bricks service, fired with #2142 and was answered imperatively (ADR-150).
Reopen for a peer whose wire shape fits the tagged server path — an
`application/jose` body, and a response sealed only when the request verified —
together with a service ready to adopt it. Such a peer supplies the inputs the
design is still missing: the authentication mechanism standing in for sender
authentication (mTLS, an API token, an IP allowlist), and whether the tag or a
Policy is the right place to express it once a reviewer can see a real threat
model.

## Prior requests

- #1577 — "jose: tag scanner mode=bare for struct-tagged routes"
- #2142 — an inbound peer sending a detached-signature JWS-of-JWE request and
  expecting bare-sealed replies; answered without a tag key, by the
  `Policy`-level door served imperatively (ADR-150, recipe #2165)

Issue #1575, which shipped the bare-JWE library surface, originally deferred
this with "if that is cheap; otherwise Policy-only is fine". It is not cheap,
for the parser-ordering and inbound-envelope reasons above, so Policy-only
stands.
