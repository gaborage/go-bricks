// Package testing provides comprehensive testing utilities for the go-bricks framework.
//
// This package contains mocks and fixtures that enable developers to write
// effective unit and integration tests for applications built with go-bricks.
//
// # Mocks
//
// The mocks subpackage provides testify-based mock implementations for all
// major framework interfaces:
//   - Database operations (database.Interface, database.Statement, database.Tx)
//   - Messaging operations (messaging.Client, messaging.AMQPClient, messaging.RegistryInterface)
//
// # Fixtures
//
// The fixtures subpackage provides helper functions and pre-configured mocks
// for common testing scenarios:
//   - Database fixtures with common behaviors (healthy, failing, with data)
//   - Messaging fixtures for simulating message flows
//   - SQL result builders for consistent test data
//
// # Usage
//
// Import the specific subpackages you need:
//
//	import (
//		"github.com/gaborage/go-bricks/testing/mocks"
//		"github.com/gaborage/go-bricks/testing/fixtures"
//	)
//
// For comprehensive examples and usage patterns, see the examples/testing
// directory and TESTING.md documentation.
package testing
