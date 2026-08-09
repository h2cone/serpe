package shared

import (
	"testing"

	"github.com/h2cone/serpe/core/models"
)

func TestMergeInstructions(t *testing.T) {
	t.Parallel()
	instructions := []models.Instruction{
		{Role: models.InstructionSystem, Text: "system"},
		{Role: models.InstructionDeveloper, Text: "developer"},
	}
	if text, requiresLenient := MergeInstructions(instructions, false); !requiresLenient || text != "" {
		t.Fatalf("strict merge = %q, %v", text, requiresLenient)
	}
	want := "[system]\nsystem" + instructionBoundary + "[developer]\ndeveloper"
	if text, requiresLenient := MergeInstructions(instructions, true); requiresLenient || text != want {
		t.Fatalf("lenient merge = %q, %v; want %q", text, requiresLenient, want)
	}
}
