package routing

import (
	"testing"
)

func TestModelRouterBasic(t *testing.T) {
	router := NewModelRouter(map[ModelRole]string{
		RoleCheap:      "qwen2.5-coder:1.5b",
		RoleGeneration: "qwen2.5-coder:7b",
		RoleReasoning:  "relational/VULCAN",
	})

	if router.ModelForStep(StepClassify) != "qwen2.5-coder:1.5b" {
		t.Errorf("classify -> cheap, got %s", router.ModelForStep(StepClassify))
	}
	if router.ModelForStep(StepRetrieve) != "qwen2.5-coder:1.5b" {
		t.Errorf("retrieve -> cheap, got %s", router.ModelForStep(StepRetrieve))
	}
	if router.ModelForStep(StepSummarize) != "qwen2.5-coder:1.5b" {
		t.Errorf("summarize -> cheap, got %s", router.ModelForStep(StepSummarize))
	}
	if router.ModelForStep(StepGenerate) != "qwen2.5-coder:7b" {
		t.Errorf("generate -> generation, got %s", router.ModelForStep(StepGenerate))
	}
	if router.ModelForStep(StepValidate) != "relational/VULCAN" {
		t.Errorf("validate -> reasoning, got %s", router.ModelForStep(StepValidate))
	}
	if router.ModelForStep(StepReason) != "relational/VULCAN" {
		t.Errorf("reason -> reasoning, got %s", router.ModelForStep(StepReason))
	}
}

func TestModelRouterFallback(t *testing.T) {
	router := NewModelRouter(map[ModelRole]string{
		RoleGeneration: "qwen2.5-coder:7b",
	})

	if router.ModelForStep(StepClassify) != "qwen2.5-coder:7b" {
		t.Errorf("fallback to generation, got %s", router.ModelForStep(StepClassify))
	}
	if router.ModelForStep(StepGenerate) != "qwen2.5-coder:7b" {
		t.Errorf("generate direct, got %s", router.ModelForStep(StepGenerate))
	}
}

func TestModelRouterEmpty(t *testing.T) {
	router := NewModelRouter(map[ModelRole]string{})
	if router.ModelForStep(StepGenerate) != "" {
		t.Errorf("empty router should return empty string")
	}
}

func TestModelRouterCustomMapping(t *testing.T) {
	router := NewModelRouter(map[ModelRole]string{
		RoleCheap:      "model-a",
		RoleGeneration: "model-b",
		RoleReasoning:  "model-c",
	})

	tests := []struct {
		step     StepType
		expected string
	}{
		{StepClassify, "model-a"},
		{StepRetrieve, "model-a"},
		{StepSummarize, "model-a"},
		{StepGenerate, "model-b"},
		{StepValidate, "model-c"},
		{StepReason, "model-c"},
	}

	for _, tc := range tests {
		if got := router.ModelForStep(tc.step); got != tc.expected {
			t.Errorf("step %s: expected %s, got %s", tc.step, tc.expected, got)
		}
	}
}

func TestModelRouterRoleModels(t *testing.T) {
	router := NewModelRouter(map[ModelRole]string{
		RoleCheap: "model-a",
	})
	models := router.RoleModels()
	if models[RoleCheap] != "model-a" {
		t.Errorf("RoleModels should return copy")
	}
	// Modify copy shouldn't affect original
	models[RoleCheap] = "model-b"
	if router.RoleModels()[RoleCheap] != "model-a" {
		t.Errorf("RoleModels should return independent copy")
	}
}

func TestModelRouterSetRoleModel(t *testing.T) {
	router := NewModelRouter(nil)
	router.SetRoleModel(RoleCheap, "new-model")
	if router.ModelForStep(StepClassify) != "new-model" {
		t.Errorf("SetRoleModel should work")
	}
}
