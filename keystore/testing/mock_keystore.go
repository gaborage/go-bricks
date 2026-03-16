// Package testing provides test utilities for the keystore package.
// It includes MockKeyStore for unit testing modules that depend on app.KeyStore.
package testing

import (
	"crypto/rsa"
	"fmt"
	"sync"
)

// MockKeyStore implements app.KeyStore for unit testing.
// Use the fluent builder methods to configure keys and error behavior.
//
// Example:
//
//	mock := kstest.NewMockKeyStore().
//	    WithPublicKey("signing", pubKey).
//	    WithPrivateKey("signing", privKey)
//
//	deps := &app.ModuleDeps{
//	    KeyStore: mock,
//	}
type MockKeyStore struct {
	mu          sync.RWMutex
	publicKeys  map[string]*rsa.PublicKey
	privateKeys map[string]*rsa.PrivateKey
	publicErr   error
	privateErr  error
}

// NewMockKeyStore creates an empty MockKeyStore.
func NewMockKeyStore() *MockKeyStore {
	return &MockKeyStore{
		publicKeys:  make(map[string]*rsa.PublicKey),
		privateKeys: make(map[string]*rsa.PrivateKey),
	}
}

// WithPublicKey adds a public key for the given name.
func (m *MockKeyStore) WithPublicKey(name string, key *rsa.PublicKey) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publicKeys[name] = key
	return m
}

// WithPrivateKey adds a private key for the given name.
func (m *MockKeyStore) WithPrivateKey(name string, key *rsa.PrivateKey) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.privateKeys[name] = key
	return m
}

// WithPublicKeyError configures all PublicKey calls to return this error.
func (m *MockKeyStore) WithPublicKeyError(err error) *MockKeyStore {
	m.publicErr = err
	return m
}

// WithPrivateKeyError configures all PrivateKey calls to return this error.
func (m *MockKeyStore) WithPrivateKeyError(err error) *MockKeyStore {
	m.privateErr = err
	return m
}

// PublicKey implements app.KeyStore.
func (m *MockKeyStore) PublicKey(name string) (*rsa.PublicKey, error) {
	if m.publicErr != nil {
		return nil, m.publicErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.publicKeys[name]
	if !ok {
		return nil, fmt.Errorf("mock keystore: public key %q not found", name)
	}
	return key, nil
}

// PrivateKey implements app.KeyStore.
func (m *MockKeyStore) PrivateKey(name string) (*rsa.PrivateKey, error) {
	if m.privateErr != nil {
		return nil, m.privateErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.privateKeys[name]
	if !ok {
		return nil, fmt.Errorf("mock keystore: private key %q not found", name)
	}
	return key, nil
}
