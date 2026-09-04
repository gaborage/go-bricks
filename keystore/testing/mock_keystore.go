// Package testing provides test utilities for the keystore package.
// It includes MockKeyStore for unit testing modules that depend on app.KeyStore.
package testing

import (
	"bytes"
	"crypto/rsa"
	"fmt"
	"slices"
	"sync"

	"github.com/gaborage/go-bricks/keystore"
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
	generations map[string][]keystore.Generation
	publicErr   error
	privateErr  error
	secretErr   error
	recorded    [][2]string
}

// RecordResolution implements keystore.RoleRecorder: the mock remembers every
// (entry, role) a startup resolution tagged, in call order, so a test can assert
// which entries the module under test claimed and under which role.
func (m *MockKeyStore) RecordResolution(entry, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded = append(m.recorded, [2]string{entry, role})
}

// Recorded returns the (entry, role) pairs RecordResolution received, in order.
func (m *MockKeyStore) Recorded() [][2]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.recorded)
}

// NewMockKeyStore creates an empty MockKeyStore.
func NewMockKeyStore() *MockKeyStore {
	return &MockKeyStore{
		publicKeys:  make(map[string]*rsa.PublicKey),
		privateKeys: make(map[string]*rsa.PrivateKey),
		secrets:     make(map[string][]byte),
		generations: make(map[string][]keystore.Generation),
	}
}

// WithGeneration declares one provisioned generation of a Logical kid. The
// mock applies no grammar, so a test controls the exact accept set the module
// under test sees, but it keeps the FamilyEnumerator ordering contract:
// Generations returns ascending versions whatever the declaration order. Pair
// it with WithPublicKey and friends on the generation's Kid() when the module
// also fetches material.
func (m *MockKeyStore) WithGeneration(logical, version string, role keystore.Role) *MockKeyStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generations[logical] = append(m.generations[logical], keystore.Generation{Logical: logical, Version: version, Role: role})
	slices.SortFunc(m.generations[logical], keystore.CompareGenerations)
	return m
}

// Generations implements keystore.FamilyEnumerator.
func (m *MockKeyStore) Generations(logical string) []keystore.Generation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.generations[logical])
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
