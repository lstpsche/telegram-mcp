// Package telegram is the only package allowed to depend on gotd generated
// Telegram types. Those types and access hashes must not cross this boundary.
// It also owns the Test-DC client, authentication flows, bounded request
// middleware, Keychain sessions, bounded text normalization and update metadata.
package telegram
