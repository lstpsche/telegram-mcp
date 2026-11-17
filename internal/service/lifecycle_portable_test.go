//go:build linux || windows

package service

import "testing"

func lifecycleFixture(t *testing.T) *Manager { m, _, _ := portableFixture(t); return m }
