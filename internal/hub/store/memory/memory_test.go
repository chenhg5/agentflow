package memory_test

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
	"github.com/chenhg5/agentflow/internal/hub/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) app.Store { return memory.New() })
}
