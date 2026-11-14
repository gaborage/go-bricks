# PostgreSQL test container: post-start hook, external-DSN mode, raw customizers

**Decision:** Rejected. `testing/containers` does not take a caller-supplied
post-start hook, does not offer a mode that skips the container in favor of an
externally provided DSN, and does not pass raw testcontainers customizers
through its config. Server settings and a tmpfs data directory were accepted
from the same request, as typed config fields.

**Reason:**

**Post-start hook.** The fixture already hands back everything a setup step
needs: the connection string, host and port. The shared-container helper takes
a caller-supplied start function, so a `TestMain` wraps the PostgreSQL start
and runs its roles, schemas and migrations right after it. The framework's own
role-separation and migration integration tests do exactly that through an
admin connection. A hook field would be a second way to spell composition the
package already supports, and none of the sibling containers takes caller
code: where a fixture needs a post-start step (enabling a broker plugin,
adding a broker user) the fixture owns it.

**External-DSN mode.** With no container there is nothing for this package to
manage. The per-test database helper would run `CREATE DATABASE` and
`DROP DATABASE` against a server someone else owns, and the handle's host and
port accessors have no container to ask. Choosing between "start a container"
and "use the server in this environment variable" is a decision about a
project's own test environment, and the branch belongs in that project's
harness, in front of the framework fixture.

**Raw customizer passthrough.** No exported API in `testing/containers` uses a
testcontainers type, the same boundary ADR-034 draws for the HTTP engine. The
PostgreSQL config type also compiles in the default build, while testcontainers
is compiled only under the `integration` build tag; a customizer field would
pull it into every build. The Redis fixture sets the pattern instead: a typed
field per capability (cluster, TLS, ACL), turned into container options
inside the fixture.

**Triggers to revisit:**

- A second sibling container needs caller code after start, which would make a
  shared hook a pattern and not a one-off.
- `testing/containers` gains a non-container backend for another reason.

## Prior requests

- #2404 — "testing/containers: customizers for the PostgreSQL container"
