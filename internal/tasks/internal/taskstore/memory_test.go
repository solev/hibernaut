package taskstore_test

import (
	"testing"

	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) taskstore.Store { return taskstore.NewMemory() })
}
