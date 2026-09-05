package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// staticBrokerURLProvider is a minimal messaging.BrokerURLProvider stub. Close causes both
// methods under test to fail before any URL lookup happens, so BrokerURL is never actually
// invoked here; it exists only so NewMessagingManager gets a real, exported-interface value
// instead of an untyped nil.
type staticBrokerURLProvider struct{}

func (staticBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	return "", errors.New("staticBrokerURLProvider: no broker configured for this test")
}

// TestMessagingManagerErrManagerClosedMatchesFromExternalPackage pins the exported sentinel
// (messaging.ErrManagerClosed, PR #950) from OUTSIDE the package: a caller that only has the
// public API — no access to the unexported field internals whitebox tests in this package can
// reach — must still be able to distinguish "manager is gone" via errors.Is. Before this fix
// the sentinel was unexported, so external callers had no way to match it. EnsureConsumers and
// Publisher both return it once Close has run.
func TestMessagingManagerErrManagerClosedMatchesFromExternalPackage(t *testing.T) {
	manager := messaging.NewMessagingManager(
		staticBrokerURLProvider{},
		logger.New("error", false),
		messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		nil, // default client factory; never invoked once the manager is closed
	)
	require.NoError(t, manager.Close())

	t.Run("ensure consumers after close", func(t *testing.T) {
		err := manager.EnsureConsumers(context.Background(), "any-key", messaging.NewDeclarations())
		require.Error(t, err)
		assert.ErrorIs(t, err, messaging.ErrManagerClosed)
	})

	t.Run("publisher after close", func(t *testing.T) {
		pub, release, err := manager.Publisher(context.Background(), "any-key")
		require.Error(t, err)
		require.ErrorIs(t, err, messaging.ErrManagerClosed)
		assert.Nil(t, pub)
		assert.Nil(t, release)
	})
}
