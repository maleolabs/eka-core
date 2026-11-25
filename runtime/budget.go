package runtime

// ContextBudget implements strata-priority truncation for LLM context
// (sto:budget-tooloutput-cache-real — hollow gap fix, feedback #31).
// Priority: constraints > dependencies > decisions > planning > downstream > history.
// byte/4 est, truncated:true guard if dropped.constraints>0.
// This is the minimal additive stub; full implementation follows Wave2 spec.

type ContextBudget struct {
	MaxBytes int
	MaxUnits int
}

func (b ContextBudget) Priority() []string {
	return []string{"constraints", "dependencies", "decisions", "planning", "downstream", "history"}
}

func (b ContextBudget) EstimateBytes(jsonLen int) int { return jsonLen / 4 }

// Truncated reports whether truncation dropped constraints (guard).
func (b ContextBudget) Truncated(droppedConstraints int) bool { return droppedConstraints > 0 }
