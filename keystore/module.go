package keystore

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// KeystoreModule implements the GoBricks Module interface for RSA key pair management.
// It loads named RSA key pairs at startup and provides them to other modules via deps.KeyStore.
//
// Register before modules that need keys:
//
//	fw.RegisterModules(
//	    keystore.NewKeystoreModule(),
//	    &myapp.JWEModule{},
//	)
//
//nolint:revive // Intentional stutter for clarity - follows GoBricks naming pattern (e.g., scheduler.SchedulerModule)
type KeystoreModule struct {
	logger logger.Logger
	store  app.KeyStore
}

// NewKeystoreModule creates a new KeystoreModule instance.
func NewKeystoreModule() *KeystoreModule {
	return &KeystoreModule{}
}

// Name implements app.Module.
func (m *KeystoreModule) Name() string {
	return "keystore"
}

// Init implements app.Module.
// Loads all configured key pairs and validates them. Fails fast on any error.
func (m *KeystoreModule) Init(deps *app.ModuleDeps) error {
	m.logger = deps.Logger

	cfg := deps.Config.KeyStore
	if len(cfg.Keys) == 0 {
		m.logger.Info().Msg("KeyStore module: no keys configured (keystore.keys is empty)")
		return nil
	}

	if err := validateConfig(&cfg); err != nil {
		return err
	}

	s, err := newStore(cfg.Keys)
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
func (m *KeystoreModule) KeyStore() app.KeyStore {
	return m.store
}

// RegisterRoutes implements app.Module. KeyStore has no HTTP routes.
func (m *KeystoreModule) RegisterRoutes(_ *server.HandlerRegistry, _ server.RouteRegistrar) {}

// DeclareMessaging implements app.Module. KeyStore has no messaging.
func (m *KeystoreModule) DeclareMessaging(_ *messaging.Declarations) {}

// Shutdown implements app.Module.
func (m *KeystoreModule) Shutdown() error {
	m.logger.Info().Msg("KeyStore module shut down")
	return nil
}
