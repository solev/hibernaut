//go:build e2e

package e2e

import (
	"testing"

	"github.com/solev/hibernaut/internal/engine/enginetest"
)

// TestEngineContract runs the contract the fake meets against real E2B, so the two can't drift.
func TestEngineContract(t *testing.T) {
	enginetest.Run(t, enginetest.Config{Engine: vm.engine, Template: template, Metadata: labels})
}
