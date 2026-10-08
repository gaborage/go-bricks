# ADR-144: Keystore Entry Names Are Dotted Paths, Written Nested

**Status:** Accepted
**Date:** 2026-10-04
**Amends:** [ADR-090](adr_090_env_reachable_section_names.md) §1 and §4 for `keystore.keys` only · [ADR-097](adr_097_sealed_amqp_messages.md) §3 (G4 grammar and the Env door)
**Related:** [ADR-024](adr_024_config_key_flatsmush.md) · [ADR-039](adr_039_composite_resolver_order.md) · [ADR-064](adr_064_app_validates_every_config.md) · [ADR-107](adr_107_jose_bare_jwe_mode.md) · [ADR-111](adr_111_jose_jws_of_jwe_mode.md)

## Context

`Load` maps an environment variable to a config key by lowercasing the name and turning every
`_` into `.` (`envVarToKey`). ADR-090 made that transform injective over the user-named maps by
restricting their keys to `^[a-z0-9-]+$`. For `keystore.keys` that leaves a gap: every
multi-word name needs a `-`, and a POSIX shell cannot `export` a variable whose name contains
`-`. `tokens-our` can be set as `KEYSTORE_KEYS_TOKENS-OUR_PRIVATE_VALUE` from a Docker or
Kubernetes manifest, or passed to a child with `env`, but never by a shell assignment or
`export`; `payments-sign-v1` is in the same position. The sealing
selector has the same gap (`MESSAGING_SEAL_ACTIVE_PAYMENTS-SIGN`), which ADR-097's Env door
accepted.

Verified on `main` through the real `Load`:

- **Nested YAML** `keystore.keys.tokens.our.public.value`, and the POSIX variable
  `KEYSTORE_KEYS_TOKENS_OUR_PUBLIC_VALUE`, both produce a phantom entry `tokens`. `our` is
  dropped, and the error names `keystore.keys.tokens.public`, the wrong key.
- **Silent loss.** YAML `tokens.public` plus a nested `tokens.our.public` loads green, with `our`
  gone. `buildDecoderConfig` leaves `ErrorUnused` off, so anything under an entry that is not
  a field is discarded.
- **Selector.** `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2` aborts decode:
  `'messaging.seal.active[payments]' expected type 'string', got map`.
- **Quoted key.** A quoted YAML key `"tokens.our"` decodes as a literal map key. Only the dot ban
  in `checkKeyStore` refuses it.

A keystore name is not only config. It is the `app.KeyStore` argument and the `jose:` tag
value. It is also the wire `kid` and, for a sealing generation, the inner `iss`. Its family is
the first half of the `gobricks_inbox` key `<family>:<jti>`. The generation grammar
(`-v<N>`) was written out four times, each with a keep-in-sync comment, until
`internal/keyname` took them over without changing them; `Generation.Kid()` still hard-coded
the `-`.

## Decision

**A `keystore.keys` entry name is a path of segments. YAML writes it as nested maps, and the
environment reaches the same path through the transform, which is unchanged. The segments
joined with `.` form the name, and that string is used verbatim everywhere.**
`KEYSTORE_KEYS_TOKENS_OUR_PRIVATE_VALUE` and `keystore.keys: {tokens: {our: {private: {value: …}}}}`
are one koanf path, so they are one entry, `tokens.our`. That string is:

- the `Keys` map key;
- the `app.KeyStore` argument;
- the `jose:` tag value;
- the wire kid.

Nothing rewrites one name into another: `tokens-our` and `tokens.our` are two names. Every
name valid today is a one-segment name under this grammar, so it keeps its meaning, its
variable and its wire kid. Reading the nested path as the name is decoding, not renaming, so
ADR-090 §2 (check, not normalize) and §6 (the transform is untouched) both stand.

### 1. Grammar, in one package

`internal/keyname` (stdlib only) holds every rule below. `config`, `keystore`, `jose`,
`jose/sealed` and `internal/sealcli` call it.

| Rule | Definition |
| --- | --- |
| Entry name (`keystore.keys`) | One or more segments of `[a-z0-9-]+`, joined by `.` (`keyname.ValidName`). No empty segment. `public`, `private`, `secret` and `pkcs12` may not follow a `.` (`keyname.ReservedAfterDot`). No length cap, as under ADR-090. |
| Selector key (`messaging.seal.active`) | The same segments, with no reserved words. |
| jose kid (`jose.ValidKid`, `jose:` tags) | `^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`: dots are allowed only between non-empty runs. |
| Logical kid (family) | A jose kid of at most 64 bytes that is not itself a generation candidate. |
| Generation candidate | A name ending in `-v<digits>`, or a name of two or more segments whose last segment is `v<digits>`. |
| Generation | `<family>.v<N>` when the family contains `.`, and `<family>-v<N>` when it does not (`keyname.GenerationName`). `N` matches `[1-9][0-9]*`. |
| Fold | Replace every `-` with `.` (`keyname.Fold`), as a POSIX variable reads a name. Fold is used only to refuse look-alikes (`keyname.FirstFoldClash`) and nesting families or selectors (`keyname.FirstFoldedPrefix`), never to resolve a name. |

**Why a field name is reserved after a `.`:** the tree reader (§2) recognizes an entry by its
fields, so `webhook.secret` would read as entry `webhook` with a `secret` field. A first
segment is never ambiguous, because every child of `keystore.keys` is a name. A legacy entry
named `secret` therefore stays legal. `databases` and `multitenant.tenants` keep
`keyname.ValidSectionName` (`^[a-z0-9-]+$`) unchanged.

### 2. Reading the tree (decode)

One mapstructure DecodeHook, `keystoreTreeHook`, is placed first in both `buildDecoderConfig`
and `unmarshalDecoderConfig`. mapstructure runs it at every nested decode, and it acts on three
target types. An entry map (`map[string]KeyPairConfig`, or of pointers) has the node it is
decoded from replaced by a flat map keyed by dotted names; a `SealConfig` has its `active`
subtree replaced the same way. mapstructure then decodes that map into `map[string]KeyPairConfig`
or `map[string]string` exactly as before, through the same guard hooks. Because the hook keys on
the entry map rather than on `KeyStoreConfig`, `Config.Unmarshal("keystore.keys", &m)` reads
dotted names as `Load` does. One `KeyPairConfig` is the third target: a node decoded into it that
holds a child no entry field names is refused, so `Config.Unmarshal` of a namespace's path
(`keystore.keys.tokens`, holding `tokens.our`) fails instead of yielding an empty entry. A
`map[string]string` is no keystore type, so `Config.Unmarshal` of the selector map alone is not
read; mapstructure refuses a nested selector there, a map where a string belongs. Unmarshal
`SealConfig` instead. Children are sorted at every level, and the input tree is never mutated. No
exported type changes.

How the `keys` subtree is read:

- **Sequences.** A YAML sequence where the entry map belongs is refused, empty or not:
  mapstructure's weak decoding would merge its maps into the entry map and skip the walk, so a
  namespace would decode as a phantom entry. A sequence where the selector map belongs is
  refused for the same reason. Both decoded as a map before this ADR. Decode sees only the
  sequence that survives the merge: a map a later layer sets on the same path (the env
  overlay, or a variable) replaces it, and what it held was dropped in silence. So each
  operator layer's sequences are recorded before the merge, at `keystore.keys` and every name
  below it down to the first entry, and anywhere under `messaging.seal.active`, and
  `config.Validate` refuses one that never reached decode (`checkLayerSequences`). Inside an
  entry the field schema governs: a later layer that sets a source over a sequence there drops
  no entry.
- **Bad keys.** A child key containing `.` (a quoted YAML key, or a literal key in a nested
  `LoadFromMap` tree), and an empty key, are refused: write the name nested instead.
- **Entries.** A node with at least one child named `public`, `private`, `secret` or `pkcs12`
  is an *entry*. Any other child of an entry is refused. It is reported as a prefix conflict
  when its subtree holds an entry, and as an unknown field otherwise.
- **Below a field,** keys are checked against the struct tags, matched as mapstructure matches
  them (case-insensitively). A source takes `file` and `value`; `pkcs12` also takes
  `password`, which takes `env` and `file`. An unknown key is refused, and so is a value of the
  wrong shape (a scalar where a source map belongs, a map where a value belongs). This turns
  `ErrorUnused: false` off for this subtree only.
- **Namespaces.** A map node with no field child is a *namespace*: each of its children is
  another name segment, and a scalar there is refused.
- **Empty nodes.** A null or empty node becomes an entry with no fields, so today's `key source
  required` error still fires.

How the `active` subtree is read:

- A scalar (or null) leaf at path `p` becomes the selector `join(p, ".")`.
- Non-empty maps are namespaces. An empty map is neither a selector nor a namespace, so it is
  refused at its path, as mapstructure refused it before this ADR.
- Literal dotted keys and empty keys are refused. A literal key with an empty segment (`"."`,
  `"tokens..our"`) is refused with a plain rename, since no nested form or variable spells it.

The walk lives in decode because decode is the only place the nested tree exists. The rules
that judge the final set (§3) live in `check`, so a hand-built `Config` (ADR-064) meets them
too. A hook error is a `*ConfigError`. mapstructure v2.5.0 wraps it in a `DecodeError` that
unwraps, and joins struct-field errors with `errors.Join`. `errors.As` therefore reaches
`Field` and `Action` through both `Load` and `Config.Unmarshal`; a test pins both doors. The
hook names a `Field` from the root `Load` meets (`keystore.keys`, `messaging.seal.active`), but
fires on its types wherever they are decoded, so `Config.Unmarshal` rewrites the `Field` to the
path it decoded, from the `DecodeError`'s node name: `custom.keys.tokens`, not
`keystore.keys.tokens`. A custom section decoded into one of these types is read as the
keystore is, so a key there that is no entry field (`kid:` beside `public:`) is refused.

### 3. Injectivity (check)

`checkKeyStore` and `checkMessagingSeal` drop their dot bans and enforce these rules, in
sorted name order, before any entry's sources are judged:

1. **Grammar** of each name (§1).
2. **Prefix-free entries.** No entry name may be a dotted prefix of another (`tokens` and
   `tokens.our`). The silent loss above becomes a startup error, for decoded and hand-built
   configs alike. The walk reports a decoded pair with its own text (§6); this rule is what a
   hand-built `Config` meets.
3. **No look-alike entries.** Two names with equal folds are refused, for example
   `tokens-our` with `tokens.our`, or `a-b.c` with `a.b-c`. Without this rule, a POSIX override
   of a hyphenated entry boots green. Example: `KEYSTORE_KEYS_TOKENS_OUR_PUBLIC_VALUE` and
   `_PRIVATE_VALUE` set beside YAML `tokens-our`. They create a complete but unused
   `tokens.our`, and the override never applies. A pair of generation names
   (`payments-sign-v1` with `payments.sign.v1`) is left to rule 4: it is a family look-alike or
   a malformed name, and moving a generation into the other family is a drain-then-cutover,
   not the in-place rename this rule's message offers. A generation name never folds equal to
   an ordinary one.
4. **Families**, derived from generation entries:
   - a malformed generation candidate is refused, with the rename spelled out — at `Validate`
     now, where before only the keystore refused it, at `Init`;
   - two families with equal folds are refused, because `payments-sign-v1` beside
     `payments.sign.v2` is a second family, not a rotation;
   - no family may be a dotted prefix of another after folding `-` to `.`, when either family
     contains `.` (`payments` and `payments.sign`; `payments-sign` and `payments.sign.eu`). Two
     families without `.` are exempt (`payments-sign` beside `payments-sign-eu`): neither has a
     nested path, and such a pair boots before this ADR.
5. **Selectors.** Selector keys follow §1, no two may have equal folds, and no two may nest by
   the folded prefix rule of 4, with the same exemption. A cross-section check,
   `checkSealSelectorFamilies`, then runs after both sections. It refuses a selector whose fold
   equals a provisioned family that it does not itself equal. Example:
   `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2` beside family `payments-sign`. That flip would
   select nothing and leave the old generation sealing. A selector that names nothing
   provisioned is still accepted, as before.

The folded prefix rules in 4 and 5 are load-bearing. A POSIX variable reads `-` as `.`, so
`MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN`, meant for family `payments-sign`, reaches the path
`payments.sign`. When the selectors are merged, a scalar on a path and a map at the same path
replace one another in silence:

- `mergeSkippingScalarOverMap` drops an env scalar that lands on a YAML map. With a selector
  for `payments.sign.eu` written nested, the flip never exists, and rule 5's cross-section
  check has nothing to refuse.
- An incoming env map replaces a YAML scalar, so a YAML selector `payments: v1` disappears
  under `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN`.

Once families nest neither as written nor after folding, no valid config needs both
selectors, so the selector rule refuses the stale or mistyped one, whatever is provisioned.
Judged on the merged section, though, it sees only a pair that survived as two paths: a
folded pair (`payments-sign` beside `payments.sign.eu`). Two selectors that nest as written
(`payments.sign` and `payments.sign.eu`, or `payments` and `payments.sign`) share one path,
a scalar and a map, so at most one reaches the tree, and the family the lost one named boots
with no selector. Sealing then fails at `Init` with `no messaging.seal.active.payments.sign
selector`, naming a selector that was written. The rule therefore also reads what each layer
offered, before the merge (`checkSelectorLayers`): `Load` records every selector name a YAML
file holds as that file is merged, and every `MESSAGING_SEAL_ACTIVE_*` variable as the
environment provider reads it, since two variables on one path already collide in the
provider's unflatten. Any two of those that nest, as written or after folding, fail startup
with `selectors "payments.sign" and "payments.sign.eu" nest: one path cannot hold both, and
loading kept only one of them`. A hand-built `Config` has no layers and meets only the rule
on its section.

### 4. Generations

The family fixes the marker:

- a family that contains `.` names its generations with a final segment: `payments.sign.v1`;
- a family without `.` keeps `-v<N>`: `payments-sign-v1`.

`Generation.Kid()` is `keyname.GenerationName(Logical, Version)`, a pure function. `Generation`
gains no field, and `keystore/testing.WithGeneration` keeps its signature and output.

Refused at startup, each with its rename:

- `payments.sign-v1` → rename to `payments.sign.v1`;
- `audit.v1` and `payments-sign.v1`, whose families have no `.` → rename to `audit-v1`, or give
  the family a second segment (`audit.sign.v1`);
- `x.v0`, `x.v01`, `x.v1.v2` and `x-v1-v2`, as before.

Wire kids are parsed with the same function by the opener (rules 3 and 10), by `seal.go`'s
family check and by the CLIs. A kid is either a well-formed generation of one of the two
shapes, or `ok=false`.

A family's marker can never change. An opener running a release from before this ADR cannot
declare a dotted family, so it never receives a dotted kid for a family it declared. An
ordinary rotation still reaches it as the recoverable `SEAL_KID_UNKNOWN_GENERATION`.

### 5. Kids, tags, selectors, CLIs

- **The wire kid is the name, with no mapping layer.**
  - A dotted entry emits `kid: tokens.our` in JOSE headers.
  - A dotted generation emits `payments.sign.v1` as the sealed outer kid and inner `iss`, and
    its inbox key is `payments.sign:<jti>`. That key is at most 64 + 1 + 128 bytes, well within
    the 255-byte `event_id` column.
  - The kid sits inside the base64url header, so the compact form's `.` separators are
    unaffected. RFC 7515 §4.1.4 and RFC 7516 §4.1.6 give `kid` no structure.
  - `:` still separates the dedup key, and it still keeps sealed keys outside `WireDedupKey`'s
    grammar.
- **`jose:` tags** accept dotted kids: `jose:"decrypt=tokens.our,verify=tokens.peer"`. `..`, or a
  leading or trailing `.`, gives `JOSE_TAG_KID_INVALID`. Kids in a code-built `jose.Policy`
  were never grammar-checked and still are not.
- **`seal:` tags** take a family: `seal:"sign=payments.sign,encrypt=payments.encrypt"`. A
  generation name there, such as `sign=payments.sign.v1`, is refused with "names a generation;
  the tag takes the family `payments.sign`".
- **`messaging.seal.active`** is written nested (`active: {payments: {sign: v2}}`) or set as
  `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN=v2`.
- **seal-event and open-event:** `-sign-kid` and `-encrypt-kid` accept either generation shape.
  A family passed where a generation is expected gets "is a family, not a generation: pass
  `<family>.v<N>`", and the marker the family does not take gets the kid to pass. Exit codes
  and open-event's JSON keys are unchanged; the values may now contain dots.
- **seal-payload** stays verbatim. Its kids are partner wire kids and were only ever checked
  for being non-empty.

| Entry name | Env var (POSIX) | Wire kid | Family / inbox key |
| --- | --- | --- | --- |
| `tokens.our` | `KEYSTORE_KEYS_TOKENS_OUR_PRIVATE_VALUE` | `tokens.our` | — |
| `payments.sign.v1` | `KEYSTORE_KEYS_PAYMENTS_SIGN_V1_PRIVATE_FILE` | `payments.sign.v1` | `payments.sign` / `payments.sign:<jti>` |
| `payments-sign-v1` (unchanged) | `KEYSTORE_KEYS_PAYMENTS-SIGN-V1_PRIVATE_FILE` (no POSIX `export`) | `payments-sign-v1` | `payments-sign` / `payments-sign:<jti>` |

### 6. Messages

Every refusal is a `*ConfigError`. Its `Field` is the real koanf path, which for a dotted name
is `keystore.keys.<name>[.<field>[.<source>]]` and can be reached by an environment variable
(`envVarForKey` round-trips it).

| Shape | Field | Message → Action |
| --- | --- | --- |
| Quoted `"tokens.our":` | `keystore.keys` | `key "tokens.our" is one YAML key containing '.'` → write it nested (`tokens: {our: …}`); the nested path is what `KEYSTORE_KEYS_TOKENS_OUR_*` reaches (under `messaging.seal.active`, the one leaf variable `MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN`, no `_*`) |
| Quoted key with an empty segment (`"tokens..our":`) | `keystore.keys` | `key "tokens..our" has an empty segment` → rename it with non-empty segments, written as nested keys |
| Scalar where a name is expected | `keystore.keys.tokens.our` | `holds a value where an entry or a further name segment was expected` |
| Entry and parent | `keystore.keys.tokens` | `"tokens" is an entry (it sets public) and the parent of entry "tokens.our"` → rename one (`tokens` → `tokens.default`) |
| Entry prefix (hand-built) | `keystore.keys.tokens` | `entry "tokens" is a dotted prefix of entry "tokens.our"` |
| Unknown field | `keystore.keys.tokens.our.privte` | `unknown field "privte" in entry "tokens.our"` → an entry takes public, private, secret or pkcs12 |
| Unknown source key | `keystore.keys.tokens.our.public.vlaue` | `unknown field "vlaue"` → public takes file or value |
| Segment grammar | `keystore.keys.tokens_our` | ADR-090 text → `[a-z0-9-]` within a segment, nesting between segments: write `"tokens.our"`, which `KEYSTORE_KEYS_TOKENS_OUR_*` reaches. No spelling is offered when it would be refused too (`webhook_secret`, `audit_v1`), and a selector offers only a family, with its one variable |
| Empty segment (hand-built) | `keystore.keys` | `name "tokens..our" has an empty segment` |
| Reserved word after `.` (hand-built configs only) | `keystore.keys` | `name "webhook.secret" uses the field name "secret" after a '.'` → rename it (`webhook-secret`, `webhook.hmac`) |
| Look-alike entries | `keystore.keys.tokens.our` (the later name in sorted order) | `"tokens-our" and "tokens.our" differ only in '-' versus '.'` → keep one: override `tokens-our` with `KEYSTORE_KEYS_TOKENS-OUR_*` (Docker, Kubernetes), or rename it `tokens.our` everywhere (YAML, code, tags, partner kid) |
| Dotted family, hyphen marker | `keystore.keys.payments.sign-v1` | `a dotted family names its generations with a final v<N> segment` → rename it `payments.sign.v1` |
| Undotted family, dotted marker | `keystore.keys.audit.v1` | `family "audit" has no '.', so its generations are named audit-v<N>` → rename it `audit-v1`, or give the family a second segment (`audit.<purpose>.v1`) |
| Look-alike families (generation pairs too, same version or not) | `keystore.keys` | `families "payments-sign" (payments-sign-v1) and "payments.sign" (payments.sign.v2) differ only in '-' versus '.': a family rename is not a rotation` → keep one family: set `payments-sign-v2` with `KEYSTORE_KEYS_PAYMENTS-SIGN-V2_*` (Docker, Kubernetes) rather than a POSIX export; moving to `"payments.sign"` is a family rename, drained before the cutover |
| Nested families | `keystore.keys` | `families "payments" and "payments.sign" nest: messaging.seal.active cannot hold a selector for both`; for folds only, `families "payments-sign" and "payments.sign.eu" nest when '-' is read as '.': …` |
| Look-alike selectors | `messaging.seal.active.payments.sign` | `selectors "payments-sign" and "payments.sign" differ only in '-' versus '.'` |
| Nested selectors | `messaging.seal.active.payments.sign.eu` | `selectors "payments-sign" and "payments.sign.eu" nest when '-' is read as '.': merged from YAML and the environment, one replaces the other in silence` → keep only the selector of the provisioned family |
| Selector for a look-alike family | `messaging.seal.active.payments.sign` | `selects "payments.sign", which is not provisioned; "payments-sign" is (v1, v2); MESSAGING_SEAL_ACTIVE_PAYMENTS_SIGN reaches only payments.sign` → set the `payments-sign` selector in YAML or as `MESSAGING_SEAL_ACTIVE_PAYMENTS-SIGN`, or rename the family |
| Nested selectors, one lost in the merge | `messaging.seal.active.payments.sign.eu` | `selectors "payments.sign" and "payments.sign.eu" nest: one path cannot hold both, and loading kept only one of them` → remove the stale selector from every YAML file and variable that sets it |
| Empty selector map | `messaging.seal.active.payments` | `holds an empty map where a generation or a further name segment was expected` → set the selector to a generation (`v<N>`), or remove the key |
| Sequence | `keystore.keys` or `messaging.seal.active` | `holds a sequence where a map was expected` → write the entries (or the selectors) as a map, one key per name segment |
| Sequence a later layer replaced (`config.Validate`) | its path (`keystore.keys`, `keystore.keys.tokens`, `messaging.seal.active.payments`) | `holds a sequence where a map was expected, and a later layer replaced it, which would drop what it held` → same Action |
| Namespace unmarshaled as one entry | the unmarshaled path (`keystore.keys.tokens`) | `a keystore entry was decoded from a node holding "our", which is no entry field (public, private, secret or pkcs12)` → unmarshal an entry by its full dotted path, or the keys map |

Three message changes outside the table:

- **`validateKeySource`'s Action** names the variables (`KEYSTORE_KEYS_TOKENS_OUR_PUBLIC_FILE` /
  `_VALUE`). When the name contains `-`, it adds that such a variable can be set from Docker or
  Kubernetes but not by a POSIX `export`, while the dotted name can be set from any shell. A
  generation name gets no dotted spelling. When its fold is malformed (`audit.v1`), the Action
  says a name any shell can set is a new family (`audit.<purpose>.v1`); when the fold is a
  generation of another family (`payments.sign.v1`), it says moving to it is a family rename.
  Either way the move is drained before the cutover.
- **The keystore's not-found error** adds `configured "tokens-our" differs only in '-' versus '.'`
  when a fold matches.
- **No change** to the ADR-090 hint machinery (`keyIsEnvUnreachable`, `missingFieldAction`,
  `reattachHead`), which serves databases and tenants only. `deliveredEmptyAction` covers no
  keystore key.

## Alternatives considered

- **Reduce a dotted spelling to the hyphen name** (rewrite `.` to `-` at the config, tag and
  CLI doors). Rejected:
  - It is the normalization ADR-090 §2 forbids, and the per-key rewrite ADR-097's Env door
    rejected.
  - One key would have two visible names: configured as `tokens.our`, logged and sent as
    `tokens-our`.
  - `a-b.c` and `a.b-c` would silently become one entry.
  - A Go caller passing the dotted name would miss.
  - It never yields a dotted wire kid.
- **Mixed markers in one family** (`signing-v1` beside `signing.v2`, plus a `Generation.Name`
  field). Rejected:
  - Rotating an existing family onto a dotted kid parks the message, with the non-recoverable
    `SEAL_KID_FAMILY_MISMATCH`, on every consumer that has not upgraded.
  - `Kid()` would no longer be a function of the family.
  - The mock API would change.
  - It only helps one-segment families.
- **Accept the quoted dotted key as a second spelling.** Rejected: koanf keeps it as a raw node
  separate from the nested path, so env precedence and the ADR-104 presence record would split
  across two nodes for one name.
- **Named map types (`KeyEntries`, `SealSelectors`) with a hook, or the `mapstructure.Unmarshaler`
  door.** Rejected: the first changes exported field types for no gain over a struct-keyed
  hook; the second decodes outside the composed guard hooks.
- **Tighten unrelated edges in the same change:** no leading, trailing or double `-`; lowercase-only
  sealed families; a 128-byte cap; refusing an unprovisioned selector. Rejected: each one fails
  a config that boots today and adds no reachability.
- **A per-entry `kid:` override** separating the config name from the wire kid. Rejected: it
  gives one key a second name. Revisit only if a partner forces it.
- **Dotted names for `databases` and `multitenant.tenants`.** Out of scope: tenant IDs are also
  request-time identifiers under the resolver grammar (ADR-039).

## Consequences

- **Breaking.** Some configs that boot today now fail at startup instead of losing data silently:
  - a key under a `keystore.keys` entry that is not one of its four fields;
  - a key under a field that is not one of its sources;
  - an entry with another entry nested inside it (such as `tokens` plus `tokens.our`);
  - a `KEYSTORE_KEYS_*` variable whose path ends off a source, including a POSIX variable that
    spells a hyphenated entry while its first segment is an entry;
  - a sequence under `keystore.keys` or `messaging.seal.active`, which decoded as a map, or,
    when a later layer replaced it with a map, was dropped with what it held;
  - a nested selector map in one YAML file that a later file replaces with a scalar on its
    parent path (`payments: {sign: v1}` under an overlay's `payments: v2`). It never reached
    decode, so it booted; each file's selectors are now judged before the merge, and the pair
    nests.

  Code changes too: `Config.Unmarshal` into a `KeyPairConfig`, a map of them, a
  `KeyStoreConfig` or a `SealConfig` reads the node as the keystore does at any path, so a
  custom section that holds a key no entry field names, which loaded with the key dropped, is
  refused.

  A malformed generation name (`x-v01`, `x-v1-v2`) is refused by `config.Validate` before the
  keystore sees it, so a service that never registers the keystore module, which booted with
  one, now fails too. Error texts change in the config name and source refusals,
  `internal/sealcli`, the keystore's own grammar errors and not-found path, `jose` tag kid
  refusals and `sealed.CheckLogicalKid`; `keystore.ActiveGeneration` and
  `ErrFamilyUnprovisioned` keep their text for a family without `.`. `[C72.17]` lists each old
  and new text.
- **Additive.** Dotted names can be written as nested YAML, set from a POSIX environment, and
  used in `jose:` and `seal:` tags, `messaging.seal.active` and the CLIs. `jose.ValidKid`
  accepts interior dots; every caller that uses it as a gate inherits that widening.
- **Wire.** Upgrading without renaming anything changes no kid, `iss`, inbox key or test
  vector. A dotted kid exists only for a dotted name, which no earlier binary can configure.
  `jose/sealed/testdata/vectors_dotted.json` pins the dotted shape.
- **Renaming is an operator act, not a migration:**
  - An in-process entry is renamed in one deploy, since look-alikes cannot coexist.
  - A partner-facing JOSE kid is renamed as part of a partner key rotation, after the partner
    confirms it accepts `.`.
  - A sealing family is renamed by draining the old family, then switching producers and
    consumers together. Bodies left under the old family are refused with
    `SEAL_KID_FAMILY_MISMATCH` and parked, never processed twice. Old ledger rows age out
    through retention. A one-segment family (`signing-v1`) therefore cannot gain a
    POSIX-settable generation in place: moving it to a dotted family is that rename.

  [keystore.md](keystore.md#entry-names) and [sealing.md](sealing.md#renaming-a-family) carry
  the runbooks. The recommendation is to use dotted names for new keys and to leave live
  partner kids and live families alone.
- **Rollback.** A config holding a dotted name fails on an earlier binary, so roll back the
  config together with the code.
- **POSIX.** A name can be set with `export` exactly when it contains no `-`. Hyphenated names
  keep ADR-090 §4's runtime-dependent posture. They are valid one-segment names, not a
  compatibility shim, because they are already persisted in sealed bodies, inbox keys and
  partner kid pins.
- **`go-bricks-migrate`.** Its `--source-config` file is often the service's own config, so the
  CLI decodes it with `config.LoadFromMap`, the framework's decoder, instead of a local copy
  of the hook chain. That copy lacked the tree reader: a nested selector aborted the load and
  dotted names decoded as phantom entries. The CLI pins a released go-bricks, so it reads
  dotted names, and refuses what this ADR refuses at decode, from its pin bump to this
  release; until then it decodes with the pinned release's decoder. One input decodes apart:
  `LoadFromMap` splits a top-level key on `.`, while `Load` keeps it literal and ignores it, so
  the CLI refuses a top-level key containing `.` before decoding. It does not run
  `config.Validate`, so the layer rules (`checkSelectorLayers`, `checkLayerSequences`) are the
  service's alone; its one file is one layer.
- **Known limitation: an entry nothing references.** The rules above judge names against each
  other, never against their readers: no mechanism reports a `keystore.keys` entry that no
  route, declaration or module asks for. Take leftover variables kept after the YAML moved
  from `tokens-our` to `tokens.our`:
  - **When `tokens.our` is configured too**, the look-alike rule refuses the pair.
  - **When `tokens.our` is missing and the private half is left over**
    (`KEYSTORE_KEYS_TOKENS-OUR_PRIVATE_VALUE` alone), startup fails on the stray entry, because
    `public` is the half an RSA entry requires: `keystore.keys.tokens-our.public key source
    required`. Its Action first suggests completing `tokens-our`, which is the wrong fix here:
    remove the variable.
  - **When the public half, a complete pair, a complete `pkcs12` stanza or a `secret` is left
    over**, the stray entry is a legal entry (a public-only entry is a verify-only key) and
    loads in silence. How the missing `tokens.our` then surfaces depends on its reader:
    - a `jose:` route resolves its kids at startup, so startup fails with the look-alike hint in
      the cause;
    - a sealing declaration resolves its families at startup too, and fails naming the
      generation entry it expected;
    - an `httpclient` built `WithJOSE` passes startup: `Build` validates the policies but
      resolves no kid. Its `JOSETransport` resolves the sign and encrypt kids only when it
      seals a request body, and the decrypt and verify kids only when it opens a protected
      response (`application/jose`, or an `Envelope` it unwraps); a bodyless request or a
      plaintext error response resolves none. The first call that does fails with
      `JOSE_KID_UNKNOWN`, the look-alike hint in its cause, and a call answered with a
      plaintext `400` shows nothing. A code-built `jose.Seal` or `jose.Open` call, and a
      direct `jose/sealed` `Seal`, `Open` or `OpenDocument`, resolve on use too;
    - a lazy `PrivateKey`, `PublicKey` or `Secret` call in module code passes startup and fails
      on first use, with the look-alike hint.

  A clean boot therefore proves the names the startup readers use, never the ones resolved per
  call. Retire variables together with the YAML they override, and after a rename resolve
  every kid a `WithJOSE` client's policies name in the module's `Init` (`PrivateKey` for the
  sign and decrypt kids, `PublicKey` for the encrypt and verify kids), or make one call that
  carries a body and receives a protected `2xx`; exercise every other per-call reader the same
  way.
- **One grammar.** The four copies of the grammar are one, and the keep-in-sync comments are gone.
- **Reserved words follow `KeyPairConfig`.** Adding a field to `KeyPairConfig` reserves another
  segment word, which is a breaking change for any name that uses it. A test pins the reserved
  set to the struct's tags.

## References

- `internal/keyname/keyname.go` (the grammar)
- `config/keystore_tree.go` (`keystoreTreeHook`, the walk)
- `config/keystore_section.go` (`checkKeyStore`, `checkKeyFamilies`, `generationFamilies`, `checkSealSelectorFamilies`)
- `config/messaging_section.go` (`checkMessagingSeal`)
- `keystore/generation.go` (`Generation.Kid`, `familyOf`)
- `keystore/keystore.go` (the not-found look-alike hint)
- `jose/tag.go` (`ValidKid`)
- `jose/sealed/kid.go` (`CheckLogicalKid`, `SplitGenerationKid`)
- `internal/sealcli/spec.go`
- [ADR-090](adr_090_env_reachable_section_names.md): the reachability rule this extends
- [ADR-097](adr_097_sealed_amqp_messages.md) §3: generation grammar, selector and Env door
- [ADR-024](adr_024_config_key_flatsmush.md): the transform, unchanged
- See [migrations.md](migrations.md) `[C72.17]`.
