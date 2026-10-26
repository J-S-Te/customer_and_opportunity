package presale

import (
	"context"
	"strings"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

func (s *Service) resolveAssignmentTargets(ctx context.Context, assignees []AssignmentTarget) (map[string]AssignmentTarget, []string, []AssignmentTarget, error) {
	targets, ids, _, err := normalizeAssignmentTargets(assignees)
	if err != nil {
		return nil, nil, nil, err
	}
	if s.ownerDirectory != nil {
		resolved, err := s.ownerDirectory.Resolve(ctx, ids)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, personID := range ids {
			person, ok := resolved[personID]
			if !ok {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			name := strings.TrimSpace(person.DisplayName)
			if name == "" {
				name = personID
			}
			departments := make([]string, 0, len(person.Organizations))
			target := targets[personID]
			selectedDepartmentID := strings.TrimSpace(target.DepartmentID)
			if selectedDepartmentID == "" {
				for _, organization := range person.Organizations {
					if organization.IsPrimary {
						selectedDepartmentID = organization.ID
						break
					}
				}
				if selectedDepartmentID == "" && len(person.Organizations) > 0 {
					selectedDepartmentID = person.Organizations[0].ID
				}
			}
			inDepartment := false
			for _, organization := range person.Organizations {
				if organization.ID == selectedDepartmentID {
					inDepartment = true
				}
				if value := strings.TrimSpace(organization.Name); value != "" {
					departments = append(departments, value)
				}
			}
			if !inDepartment {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			target.DepartmentID = selectedDepartmentID
			target.PersonName = name
			target.Department = strings.Join(departments, "、")
			if len([]rune(target.PersonName)) > 128 || len([]rune(target.Department)) > 128 {
				return nil, nil, nil, ownerdirectory.ErrSelectionInvalid
			}
			targets[personID] = target
		}
	}
	canonicalTargets := make([]AssignmentTarget, 0, len(ids))
	for _, personID := range ids {
		canonicalTargets = append(canonicalTargets, targets[personID])
	}
	return targets, ids, canonicalTargets, nil
}
