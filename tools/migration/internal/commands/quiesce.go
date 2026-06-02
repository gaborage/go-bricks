package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for the control-plane *sql.DB
	"github.com/spf13/cobra"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/migration"
)

// quiesceFlags layers the quiesce-specific flags on top of the shared
// tenant/credential resolution used to locate the control-plane database.
type quiesceFlags struct {
	common *CommonFlags
	table  string
	reason string
	ttl    time.Duration
}

// NewQuiesceCommand builds the `quiesce` parent with set/clear/status children.
//
// DESIGN NOTE (needs maintainer ratification at PR time): the control-plane
// quiesce_flags table is located via the SAME tenant/credential resolution as
// the migrate commands — the operator names the control-plane target with
// --tenant and supplies its credentials. v1 assumes a single "global" scope
// row living in that database. An alternative is a dedicated control-plane
// connection flag; chosen the reuse path to avoid new credential surface.
func NewQuiesceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quiesce",
		Short: "Set, clear, or query the deployment quiesce flag",
		Long: `Manage the deployment quiesce flag in the control-plane database.

While set, provisioning workers park pending jobs and MigrateAll stops
dispatching new tenants; in-flight work drains. The flag auto-releases at its
TTL (crash-safe) and can be cleared by any operator.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newQuiesceSetCommand(), newQuiesceClearCommand(), newQuiesceStatusCommand())
	return cmd
}

// Quiesce subcommand names, named so the registration and the Use fields stay
// in sync (and to keep the repeated "status" literal out of production code).
const (
	useQuiesceSet    = "set"
	useQuiesceClear  = "clear"
	useQuiesceStatus = "status"
)

func newQuiesceSetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   useQuiesceSet,
		Short: "Activate (or renew) the deployment quiesce flag",
		Args:  cobra.NoArgs,
	}
	flags := addQuiesceFlags(cmd)
	cmd.Flags().StringVar(&flags.reason, "reason", "", "Operator-supplied reason recorded with the flag and audit event")
	cmd.Flags().DurationVar(&flags.ttl, "ttl", 0, "Auto-release TTL (0 = default 30m, clamped to 2h)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withControlPlaneController(cmd, flags, func(ctx context.Context, ctrl migration.QuiesceController) error {
			return runQuiesceSet(ctx, cmd.OutOrStdout(), ctrl, flags)
		})
	}
	return cmd
}

func newQuiesceClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   useQuiesceClear,
		Short: "Deactivate the deployment quiesce flag (operator override)",
		Args:  cobra.NoArgs,
	}
	flags := addQuiesceFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withControlPlaneController(cmd, flags, func(ctx context.Context, ctrl migration.QuiesceController) error {
			return runQuiesceClear(ctx, cmd.OutOrStdout(), ctrl, flags.common.AppliedBy, flags.common.JSON)
		})
	}
	return cmd
}

func newQuiesceStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   useQuiesceStatus,
		Short: "Show the deployment quiesce flag status",
		Args:  cobra.NoArgs,
	}
	flags := addQuiesceFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withControlPlaneController(cmd, flags, func(ctx context.Context, ctrl migration.QuiesceController) error {
			return runQuiesceStatus(ctx, cmd.OutOrStdout(), ctrl, flags.common.JSON)
		})
	}
	return cmd
}

// addQuiesceFlags attaches the shared tenant/credential flags plus --table and
// --applied-by (the audited principal), returning the quiesce flag bundle.
func addQuiesceFlags(cmd *cobra.Command) *quiesceFlags {
	common := addCommonFlags(cmd)
	qf := &quiesceFlags{common: common}
	cmd.Flags().StringVar(&qf.table, "table", migration.DefaultQuiesceTable, "Control-plane quiesce table name")
	return qf
}

// runQuiesceSet activates the flag and prints the resulting status. Separated
// from the cobra wiring so it can be unit-tested against any QuiesceController.
func runQuiesceSet(ctx context.Context, out io.Writer, ctrl migration.QuiesceController, flags *quiesceFlags) error {
	st, err := ctrl.Set(ctx, migration.QuiesceSetOptions{
		By:     flags.common.AppliedBy,
		Reason: flags.reason,
		TTL:    flags.ttl,
	})
	if err != nil {
		return fmt.Errorf("quiesce set: %w", err)
	}
	return renderStatus(out, st, flags.common.JSON, "Quiesce flag set.")
}

// runQuiesceClear clears the flag, mapping the "nothing active" sentinel to a
// clean, non-error message.
func runQuiesceClear(ctx context.Context, out io.Writer, ctrl migration.QuiesceController, by string, asJSON bool) error {
	st, err := ctrl.Clear(ctx, by)
	if errors.Is(err, migration.ErrQuiesceNotSet) {
		// No-op clear: still emit machine-readable output under --json (a
		// zero/not-quiesced status), matching the status command's shape.
		if asJSON {
			return renderStatus(out, &migration.QuiesceStatus{}, true, "")
		}
		_, werr := fmt.Fprintln(out, "No active quiesce flag to clear.")
		return werr
	}
	if err != nil {
		return fmt.Errorf("quiesce clear: %w", err)
	}
	return renderStatus(out, st, asJSON, "Quiesce flag cleared.")
}

// runQuiesceStatus prints the current status (text or JSON).
func runQuiesceStatus(ctx context.Context, out io.Writer, gate migration.QuiesceGate, asJSON bool) error {
	st, err := gate.Query(ctx)
	if err != nil {
		return fmt.Errorf("quiesce status: %w", err)
	}
	return renderStatus(out, st, asJSON, "")
}

// renderStatus prints a QuiesceStatus as text (with an optional headline) or as
// a JSON object. Extracted so the three subcommands render consistently.
func renderStatus(out io.Writer, st *migration.QuiesceStatus, asJSON bool, headline string) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(quiesceStatusJSONFrom(st))
	}
	if headline != "" {
		if _, err := fmt.Fprintln(out, headline); err != nil {
			return err
		}
	}
	state := "not quiesced"
	switch {
	case st.Active:
		state = "ACTIVE"
	case st.Expired:
		state = "expired (auto-released)"
	case st.ClearedAt != nil:
		state = "cleared"
	}
	_, err := fmt.Fprintf(out, "state=%s set_by=%q reason=%q expires_at=%s\n",
		state, st.SetBy, st.Reason, formatTime(st.ExpiresAt))
	return err
}

// quiesceStatusJSON is the stable JSON shape emitted by --json.
type quiesceStatusJSON struct {
	Active    bool   `json:"active"`
	Expired   bool   `json:"expired"`
	Cleared   bool   `json:"cleared"`
	SetBy     string `json:"setBy"`
	Reason    string `json:"reason"`
	SetAt     string `json:"setAt,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	ClearedAt string `json:"clearedAt,omitempty"`
}

func quiesceStatusJSONFrom(st *migration.QuiesceStatus) quiesceStatusJSON {
	j := quiesceStatusJSON{
		Active: st.Active, Expired: st.Expired, Cleared: st.ClearedAt != nil,
		SetBy: st.SetBy, Reason: st.Reason,
		SetAt: formatTime(st.SetAt), ExpiresAt: formatTime(st.ExpiresAt),
	}
	if st.ClearedAt != nil {
		j.ClearedAt = formatTime(*st.ClearedAt)
	}
	return j
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// controllerOpener resolves the control-plane database and returns a ready
// QuiesceController plus a close func. It is a package var so tests can inject
// an in-memory controller and exercise the full command path without a DB; the
// production value below opens a real pgx connection.
var controllerOpener = func(ctx context.Context, flags *CommonFlags, table string) (migration.QuiesceController, func(), error) {
	db, closeDB, err := openControlPlaneDB(ctx, flags)
	if err != nil {
		return nil, nil, err
	}
	ctrl, err := migration.NewPostgresQuiesceController(db, table)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	// Emit quiesce.set / quiesce.cleared through the always-on OTel seam (span +
	// structured log), mirroring how the migrate path wires migration.applied.
	ctrl.WithAudit(migration.NewEmitter(newCLILogger(flags), nil))
	return ctrl, closeDB, nil
}

// withControlPlaneController resolves flags, obtains the control-plane
// controller via controllerOpener, ensures the table exists, and invokes fn.
// The controller's resources are released on return.
func withControlPlaneController(cmd *cobra.Command, flags *quiesceFlags, fn func(context.Context, migration.QuiesceController) error) error {
	if err := resolveFlags(cmd, flags.common); err != nil {
		return err
	}
	ctx := cmd.Context()

	ctrl, closeFn, err := controllerOpener(ctx, flags.common, flags.table)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := ctrl.CreateTable(ctx); err != nil {
		return err
	}
	return fn(ctx, ctrl)
}

// openControlPlaneDB resolves the --tenant target's database config via the
// existing credential machinery and opens a pgx *sql.DB to it.
func openControlPlaneDB(ctx context.Context, flags *CommonFlags) (db *sql.DB, closeFn func(), err error) {
	if flags.Tenant == "" {
		return nil, nil, errors.New("quiesce requires --tenant naming the control-plane database target")
	}
	fileStore, err := maybeLoadFileStore(flags)
	if err != nil {
		return nil, nil, err
	}
	provider, err := buildConfigProvider(ctx, flags, fileStore)
	if err != nil {
		return nil, nil, err
	}
	dbCfg, err := provider.DBConfig(ctx, flags.Tenant)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve control-plane db config for %q: %w", flags.Tenant, err)
	}
	if dbCfg.Type != "" && dbCfg.Type != config.PostgreSQL {
		return nil, nil, fmt.Errorf("quiesce is PostgreSQL-only in v1; control-plane target is %q", dbCfg.Type)
	}

	opened, err := sql.Open("pgx", controlPlaneDSN(dbCfg))
	if err != nil {
		return nil, nil, fmt.Errorf("open control-plane db: %w", err)
	}
	if err := opened.PingContext(ctx); err != nil {
		_ = opened.Close()
		return nil, nil, fmt.Errorf("connect control-plane db: %w", err)
	}
	return opened, func() { _ = opened.Close() }, nil
}

// controlPlaneDSN builds a pgx DSN, mirroring the framework's database/postgresql
// connector: an explicit ConnectionString wins verbatim; otherwise the URL is
// assembled from the discrete fields with credentials URL-encoded (so special
// characters in the password can't corrupt the URL, and the password is never
// logged). sslmode is set only when the operator configured TLS.Mode — never a
// forced plaintext downgrade.
func controlPlaneDSN(c *config.DatabaseConfig) string {
	if c.ConnectionString != "" {
		return c.ConnectionString
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.Username, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   c.Database,
	}
	if c.TLS.Mode != "" {
		u.RawQuery = url.Values{"sslmode": {c.TLS.Mode}}.Encode()
	}
	return u.String()
}
