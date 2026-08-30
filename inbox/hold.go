package inbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gaborage/go-bricks/database"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/tenantstore"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// HoldLedger is the port the streams lane parks through, or nil when this module
// runs no hold. The lane reads a nil as "no hold configured" and refuses any
// consumer that declared one, so the two answers must not be confused.
func (m *Module) HoldLedger() streams.HoldLedger {
	if !m.holdEnabled() {
		return nil
	}
	return &holdLedger{module: m}
}

// SetHoldReplayer receives the source of the replayer the drain drives. It is a
// func rather than the value because the streams manager does not exist yet when
// modules are registered.
func (m *Module) SetHoldReplayer(src func() streams.HoldReplayer) {
	m.holdReplayer = src
}

// holdLedger adapts the module's store to the lane's port: it resolves the
// control-plane database per call, the way every other accessor here does.
type holdLedger struct {
	module *Module
}

func (l *holdLedger) Park(ctx context.Context, msg *streams.HeldMessage) error {
	db, store, err := l.module.holdStoreFor(ctx)
	if err != nil {
		return err
	}

	row, err := holdRowOf(msg)
	if err != nil {
		return err
	}

	return database.WithTx(ctx, db, func(ctx context.Context, tx dbtypes.Tx) error {
		_, parkErr := store.Park(ctx, tx, row)
		return parkErr
	})
}

func (l *holdLedger) HeldTenants(ctx context.Context, consumer string) ([]string, error) {
	db, store, err := l.module.holdStoreFor(ctx)
	if err != nil {
		return nil, err
	}
	return store.HeldTenants(ctx, db, consumer)
}

// holdRowOf renders one held message as a row. The properties are the producer's
// own map, stored as the JSON the replay decodes back.
func holdRowOf(msg *streams.HeldMessage) (*HoldRow, error) {
	var properties []byte
	if len(msg.Properties) > 0 {
		encoded, err := json.Marshal(msg.Properties)
		if err != nil {
			return nil, fmt.Errorf("inbox hold: encode properties failed: %w", err)
		}
		properties = encoded
	}

	return &HoldRow{
		Consumer:   msg.Consumer,
		Stream:     msg.Stream,
		Offset:     msg.Offset,
		TenantID:   msg.TenantID,
		Data:       msg.Data,
		Properties: properties,
		HeldAt:     msg.HeldAt,
	}, nil
}

// holdStoreFor resolves the control-plane database and this module's hold store
// together, since every ledger call needs both.
func (m *Module) holdStoreFor(ctx context.Context) (dbtypes.Interface, HoldStore, error) {
	db, err := m.getDB(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("inbox hold: resolve database failed: %w", err)
	}

	store, err := m.ensureHoldStoreInitialized(ctx)
	if err != nil {
		return nil, nil, err
	}
	return db, store, nil
}

// ensureHoldStoreInitialized mirrors the inbox ledger's lazy init: the vendor is
// only known once a connection exists. SingleKey, because a hold only ever lives
// on the control-plane database.
func (m *Module) ensureHoldStoreInitialized(ctx context.Context) (HoldStore, error) {
	return m.holdStores.Get(ctx, &tenantstore.Deps[HoldStore]{
		Name:            "inbox",
		TableName:       m.cfg.Hold.TableName,
		AutoCreateTable: m.cfg.AutoCreateTable,
		Logger:          m.logger,
		GetDB:           m.getDB,
		NewPostgres:     NewPostgresHoldStore,
		NewOracle:       NewOracleHoldStore,
		WarnMsg:         "Inbox hold table creation failed (may already exist)",
		SingleKey:       true,
	})
}

// verifyHoldDatabase proves the hold's tables are readable before anything parks
// into them: a hold that discovers its table at the first failure has already
// stalled a partition to find out.
func (m *Module) verifyHoldDatabase(ctx context.Context, db dbtypes.Interface) error {
	store, err := m.ensureHoldStoreInitialized(ctx)
	if err != nil {
		return err
	}
	if _, err := store.HeldTenants(ctx, db, ""); err != nil {
		return tenantstore.TableUnusableError("inbox", m.cfg.Hold.TableName, "inbox.autocreatetable", err)
	}
	return nil
}
