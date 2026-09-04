// Package telegram is the only package allowed to depend on gotd generated
// Telegram types. Those types and access hashes must not cross this boundary.
// Phase 1 also owns the Test-DC client, authentication flows, bounded request
// middleware, and Keychain-backed session adapter.
package telegram
