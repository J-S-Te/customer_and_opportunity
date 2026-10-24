package presale

import (
	"context"
	"errors"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

func TestExecutorReplayUsesStableIntentBeforeMutableDirectory(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, change := range []string{"name", "organization", "unavailable", "removed-and-revoked"} {
			name := change
			if legacy {
				name = "legacy/" + name
			}
			t.Run(name, func(t *testing.T) {
				nodes := ts011TwoLevelNodes(t)
				if legacy {
					nodes = ts011LegacyNodes(t)
				}
				request, instance := ts011Request(t, nodes)
				if !legacy {
					request.Status, request.TeamLeadPersonID = StatusPendingExecutionAssignment, "lead-a"
					request.ExecutionDepartmentID = "org-1"
				}
				repo := &ts011Repository{mutationRepository: &mutationRepository{request: request, approval: instance, replays: map[string]*MutationReplay{}}}
				directory := ts011NewDirectory()
				service := newTS011Service(repo, directory)
				actor := ts011TeamLead("lead-a")
				assign := service.ReplaceExecutorAssignments
				if legacy {
					actor, assign = ts011TechnicalDirector(), service.ReplaceAssignments
				}
				input := ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "exec-a", Role: "technician"}}, ChangeReason: "指派", Version: 3}
				if _, err := assign(context.Background(), actor, 9, "stable", input); err != nil {
					t.Fatal(err)
				}
				if len(repo.events) != 1 || repo.events[0].RecipientPersonID != "exec-a" || repo.events[0].RecipientNamespace != "USER" {
					t.Fatalf("platform user recipient evidence=%+v", repo.events)
				}
				switch change {
				case "name":
					user := directory.users["exec-a"]
					user.DisplayName = "变更姓名"
					directory.users["exec-a"] = user
				case "organization":
					user := directory.users["exec-a"]
					user.Organizations = []ownerdirectory.Organization{ts011Organization("org-2", "新组织")}
					directory.users["exec-a"] = user
				case "unavailable":
					directory.err = ownerdirectory.ErrUnavailable
				case "removed-and-revoked":
					next := ReplaceAssignmentsInput{Assignees: []AssignmentTarget{{PersonID: "exec-b", Role: "technician"}}, ChangeReason: "重新指派", Version: repo.request.Version}
					if _, err := assign(context.Background(), actor, 9, "next", next); err != nil {
						t.Fatal(err)
					}
					delete(directory.users, "exec-a")
					delete(directory.roles, "exec-a")
				}
				adds, updates, events := repo.assignmentAdds, repo.requestUpdates, len(repo.events)
				if value, err := assign(context.Background(), actor, 9, "stable", input); err != nil || value == nil || value.Version != repo.request.Version {
					t.Fatalf("committed replay changed after %s: value=%+v err=%v", change, value, err)
				}
				if repo.assignmentAdds != adds || repo.requestUpdates != updates || len(repo.events) != events {
					t.Fatal("replay repeated persistence")
				}
				conflict := input
				conflict.ChangeReason = "不同意图"
				if _, err := assign(context.Background(), actor, 9, "stable", conflict); !errors.Is(err, ErrIdempotencyConflict) {
					t.Fatalf("different intent with same key: %v", err)
				}
				conflict = input
				conflict.Assignees = []AssignmentTarget{{PersonID: "exec-a", Role: "technician", DepartmentID: "org-2"}}
				if _, err := assign(context.Background(), actor, 9, "stable", conflict); !errors.Is(err, ErrIdempotencyConflict) {
					t.Fatalf("different selected organization with same key: %v", err)
				}
				if !legacy {
					repo.request.TeamLeadPersonID = "lead-b"
					if _, err := assign(context.Background(), actor, 9, "stable", input); !errors.Is(err, ErrForbidden) {
						t.Fatalf("former team lead replay: %v", err)
					}
					repo.request.TeamLeadPersonID = "lead-a"
				}
				actor.Permissions = map[string]bool{}
				if _, err := assign(context.Background(), actor, 9, "stable", input); !errors.Is(err, ErrForbidden) {
					t.Fatalf("revoked caller replay: %v", err)
				}
			})
		}
	}
}
