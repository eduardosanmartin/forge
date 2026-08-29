package routing

// StepType represents the type of step in the agent turn cycle.
type StepType string

const (
	// StepClassify: intent classification, routing decisions, simple decisions
	StepClassify StepType = "classify"
	// StepRetrieve: retrieval query generation, search term extraction
	StepRetrieve StepType = "retrieve"
	// StepSummarize: conversation summarization, compaction
	StepSummarize StepType = "summarize"
	// StepGenerate: main generation (code, text, responses)
	StepGenerate StepType = "generate"
	// StepValidate: validation, verification, test generation
	StepValidate StepType = "validate"
	// StepReason: complex reasoning, planning, architecture decisions
	StepReason StepType = "reason"
)

// ModelRole represents the role a model plays in the routing.
type ModelRole string

const (
	// RoleCheap: small, fast models (1.5b/0.5b) for cheap steps
	RoleCheap ModelRole = "cheap"
	// RoleGeneration: capable models (3b/7b) for main generation
	RoleGeneration ModelRole = "generation"
	// RoleReasoning: large models (8b+) for complex reasoning
	RoleReasoning ModelRole = "reasoning"
)

// ModelRouter maps step types to models via roles.
type ModelRouter struct {
	roleModels map[ModelRole]string
}

// NewModelRouter creates a new router with the given role->model mapping.
func NewModelRouter(roleModels map[ModelRole]string) *ModelRouter {
	return &ModelRouter{roleModels: roleModels}
}

// ModelForStep returns the model name for a given step type.
// Falls back through: generation -> cheap -> reasoning.
func (r *ModelRouter) ModelForStep(step StepType) string {
	role := r.roleForStep(step)
	if model, ok := r.roleModels[role]; ok && model != "" {
		return model
	}
	// Fallback chain
	for _, fb := range []ModelRole{RoleGeneration, RoleCheap, RoleReasoning} {
		if model, ok := r.roleModels[fb]; ok && model != "" {
			return model
		}
	}
	return ""
}

// roleForStep maps a step type to its primary model role.
func (r *ModelRouter) roleForStep(step StepType) ModelRole {
	switch step {
	case StepClassify, StepRetrieve, StepSummarize:
		return RoleCheap
	case StepGenerate:
		return RoleGeneration
	case StepValidate, StepReason:
		return RoleReasoning
	default:
		return RoleGeneration
	}
}

// RoleModels returns a copy of the role->model mapping.
func (r *ModelRouter) RoleModels() map[ModelRole]string {
	m := make(map[ModelRole]string, len(r.roleModels))
	for k, v := range r.roleModels {
		m[k] = v
	}
	return m
}

// SetRoleModel sets a model for a role.
func (r *ModelRouter) SetRoleModel(role ModelRole, model string) {
	if r.roleModels == nil {
		r.roleModels = make(map[ModelRole]string)
	}
	r.roleModels[role] = model
}
