package keystore

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/logger"
)

// Module implements the GoBricks app.Module interface for named key-material management.
// It loads named RSA key pairs and raw symmetric secrets at startup and provides them to
// other modules via deps.KeyStore.
//
// Register before modules that need keys:
//
//	if err := fw.RegisterModule(keystore.NewModule()); err != nil {
//	    log.Fatal(err)
//	}
//	if err := fw.RegisterModule(&myapp.JWEModule{}); err != nil {
//	    log.Fatal(err)
//	}
type Module struct {
	logger logger.Logger
	store  app.KeyStore
}

// NewModule creates a new Module instance.
func NewModule() *Module {
	return &Module{}
}

// Name implements app.Module.
func (m *Module) Name() string {
	return "keystore"
}

// Init implements app.Module.
// Loads all configured key material (RSA pairs and symmetric secrets) and validates it. Fails fast on any error.
func (m *Module) Init(deps *app.ModuleDeps) error {
	m.logger = deps.Logger

	cfg := deps.Config.KeyStore
	if len(cfg.Keys) == 0 {
		m.logger.Info().Msg("KeyStore module: no keys configured (keystore.keys is empty)")
		return nil
	}

	s, err := newStore(cfg.Keys, cfg.SecretMinLength)
	if err != nil {
		return err
	}

	m.store = s

	m.logger.Info().
		Int("keys", len(cfg.Keys)).
		Msg("KeyStore initialized successfully")

	return nil
}

// KeyStore implements app.KeyStoreProvider.
func (m *Module) KeyStore() app.KeyStore {
	return m.store
}

// Shutdown implements app.Module.
func (m *Module) Shutdown() error {
	if m.logger != nil {
		m.logger.Info().Msg("KeyStore module shut down")
	}
	return nil
}
