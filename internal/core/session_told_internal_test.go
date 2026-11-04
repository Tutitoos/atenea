package core

import (
	"testing"

	"github.com/Tutitoos/atenea/internal/orchestrator"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// What a chat is told is not what the run found: the run still discovers it,
// and the chat is handed only the heights it holds.
func TestAChatIsToldOnlyWhatItMayRead(t *testing.T) {
	found := []contract.Discovery{
		{Level: contract.ContextRepository, Note: "repository fact"},
		{Level: contract.ContextGlobal, Note: "global fact"},
	}
	near := &Session{levels: []contract.ContextLevel{contract.ContextRepository}}
	result := &orchestrator.Result{Discoveries: found}
	near.told(result)
	if len(result.Discoveries) != 1 || result.Discoveries[0].Level != contract.ContextRepository {
		t.Fatalf("a repository-level chat was told %+v", result.Discoveries)
	}

	none := &Session{}
	result = &orchestrator.Result{Discoveries: found}
	none.told(result)
	if len(result.Discoveries) != 0 {
		t.Fatalf("a chat with no context heights was told %+v", result.Discoveries)
	}
}
