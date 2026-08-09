package shared

import (
	"strings"

	"github.com/h2cone/serpe/core/models"
)

const instructionBoundary = "\n\n--- serpe instruction boundary ---\n\n"

// MergeInstructions deterministically flattens system and developer
// instructions for protocols with one instruction layer. requiresLenient is
// true when a strict mapping encounters a developer instruction.
func MergeInstructions(instructions []models.Instruction, lenient bool) (text string, requiresLenient bool) {
	var merged strings.Builder
	for index, instruction := range instructions {
		if instruction.Role == models.InstructionDeveloper && !lenient {
			return "", true
		}
		if index > 0 {
			merged.WriteString(instructionBoundary)
		}
		if lenient {
			merged.WriteByte('[')
			merged.WriteString(string(instruction.Role))
			merged.WriteString("]\n")
		}
		merged.WriteString(instruction.Text)
	}
	return merged.String(), false
}
