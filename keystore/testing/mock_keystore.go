// Package testing provides test utilities for the keystore package.
// It includes MockKeyStore for unit testing modules that depend on app.KeyStore.
package testing

import (
	"bytes"
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
	secrets     map[string][]byte
	publicErr   error
	privateErr  error
	secretErr   error
}

// NewMockKeyStore creates an empty MockKeyStore.
func NewMockKeyStore() *MockKeyStore {
	return &MockKeyStore{
		publicKeys:  make(map[string]*rsa.PublicKey),
		privateKeys: make(map[string]*rsa.PrivateKey),
		secrets:     make(map[string][]byte),
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

// WithSecret adds raw symmetric key material for the given name. The slice is
// copied so later caller mutations do not bleed into the mock.
func (m *MockKeyStore) WithSecret(name string, secret []byte) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[name] = bytes.Clone(secret)
	return m
}

// WithSecretError configures all Secret calls to return this error.
func (m *MockKeyStore) WithSecretError(err error) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secretErr = err
	return m
}

// WithPublicKeyError configures all PublicKey calls to return this error.
func (m *MockKeyStore) WithPublicKeyError(err error) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publicErr = err
	return m
}

// WithPrivateKeyError configures all PrivateKey calls to return this error.
func (m *MockKeyStore) WithPrivateKeyError(err error) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.privateErr = err
	return m
}

// PublicKey implements app.KeyStore.
func (m *MockKeyStore) PublicKey(name string) (*rsa.PublicKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.publicErr != nil {
		return nil, m.publicErr
	}
	key, ok := m.publicKeys[name]
	if !ok {
		return nil, fmt.Errorf("mock keystore: public key %q not found", name)
	}
	return key, nil
}

// PrivateKey implements app.KeyStore.
func (m *MockKeyStore) PrivateKey(name string) (*rsa.PrivateKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.privateErr != nil {
		return nil, m.privateErr
	}
	key, ok := m.privateKeys[name]
	if !ok {
		return nil, fmt.Errorf("mock keystore: private key %q not found", name)
	}
	return key, nil
}

// Secret implements app.KeyStore. It returns a defensive copy, mirroring the
// real store so tests exercise the same ownership contract.
func (m *MockKeyStore) Secret(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.secretErr != nil {
		return nil, m.secretErr
	}
	secret, ok := m.secrets[name]
	if !ok {
		return nil, fmt.Errorf("mock keystore: secret %q not found", name)
	}
	return bytes.Clone(secret), nil
}
