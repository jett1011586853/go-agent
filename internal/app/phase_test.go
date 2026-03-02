package app

import (
	"testing"

	"go-agent/internal/message"
)

func TestNewPhaseControllerUsesConfiguredMaxFiles(t *testing.T) {
	pc := newPhaseController("complete in 3 phases", 3)
	if !pc.enabled {
		t.Fatalf("expected phase controller enabled")
	}
	if pc.total != 3 {
		t.Fatalf("expected total phases=3, got %d", pc.total)
	}
	if pc.maxFilesPerPhase != 3 {
		t.Fatalf("expected maxFilesPerPhase=3, got %d", pc.maxFilesPerPhase)
	}
	if pc.stageOrDefault() != phaseStagePlan {
		t.Fatalf("expected initial stage=plan, got %s", pc.stageOrDefault())
	}
}

func TestNewPhaseControllerFallbackMaxFiles(t *testing.T) {
	pc := newPhaseController("complete in 2 phases", 0)
	if pc.maxFilesPerPhase != 5 {
		t.Fatalf("expected fallback maxFilesPerPhase=5, got %d", pc.maxFilesPerPhase)
	}
}

func TestPhaseControllerStageTransitions(t *testing.T) {
	pc := newPhaseController("complete in 1 phase", 5)
	if !pc.enabled {
		t.Fatalf("expected enabled controller")
	}
	if err := pc.addWrites([]message.WriteRecord{{Path: "a.txt", Op: "modify"}}); err != nil {
		t.Fatalf("addWrites failed: %v", err)
	}
	if pc.stageOrDefault() != phaseStageVerify {
		t.Fatalf("expected stage=verify after writes, got %s", pc.stageOrDefault())
	}
	pc.setStage(phaseStageSummary)
	if pc.stageOrDefault() != phaseStageSummary {
		t.Fatalf("expected stage=summary, got %s", pc.stageOrDefault())
	}
	pc.closeCurrentPhase()
	if pc.stageOrDefault() != phaseStageDone {
		t.Fatalf("expected stage=done after closing last phase, got %s", pc.stageOrDefault())
	}
}

func TestPhaseControllerIgnoresMkdirWritesInFileBudget(t *testing.T) {
	pc := newPhaseController("complete in 1 phase", 1)
	if !pc.enabled {
		t.Fatalf("expected enabled controller")
	}
	if err := pc.addWrites([]message.WriteRecord{{Path: "projects/trade-lab/cmd", Op: "mkdir"}}); err != nil {
		t.Fatalf("mkdir-only writes should not fail budget: %v", err)
	}
	if got := len(pc.changedFiles()); got != 0 {
		t.Fatalf("expected no tracked core files for mkdir-only writes, got %d", got)
	}
	if pc.pendingProjectCheck {
		t.Fatalf("expected pendingProjectCheck=false for mkdir-only writes")
	}
	if pc.stageOrDefault() != phaseStagePlan {
		t.Fatalf("expected stage to remain plan, got %s", pc.stageOrDefault())
	}
}
