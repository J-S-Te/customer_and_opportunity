package portalbootstrap

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
)

type projectSnapshotResponse struct {
	ProjectID               string     `json:"project_id"`
	ProjectName             string     `json:"project_name"`
	ContractNo              string     `json:"contract_no"`
	Status                  string     `json:"status"`
	ProgressPct             uint8      `json:"progress_pct"`
	CurrentStage            string     `json:"current_stage"`
	ExpectedEndDate         *time.Time `json:"expected_end_date,omitempty"`
	Delayed                 bool       `json:"delayed"`
	ManagerName             string     `json:"manager_name"`
	ManagerContactMasked    string     `json:"manager_contact_masked"`
	ManagerMessageAvailable bool       `json:"manager_message_available"`
	SourceUpdatedAt         time.Time  `json:"source_updated_at"`
	SyncedAt                time.Time  `json:"synced_at"`
}
type projectMilestoneResponse struct {
	StageCode   string     `json:"stage_code"`
	StageName   string     `json:"stage_name"`
	Status      string     `json:"status"`
	PlannedAt   *time.Time `json:"planned_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	SortNo      int        `json:"sort_no"`
}
type projectActivityResponse struct {
	Type       string    `json:"type"`
	Content    string    `json:"content"`
	OccurredAt time.Time `json:"occurred_at"`
}
type projectTeamResponse struct {
	Name          string `json:"name"`
	Role          string `json:"role"`
	ContactMasked string `json:"contact_masked"`
}
type projectBundleResponse struct {
	Snapshot   projectSnapshotResponse    `json:"snapshot"`
	Milestones []projectMilestoneResponse `json:"milestones"`
	// Activities remains an empty compatibility field. Complete history is
	// available only from GET /projects/{projectID}/activities.
	Activities []projectActivityResponse `json:"activities"`
	Team       []projectTeamResponse     `json:"team"`
}

func publicProjectSnapshot(value *project.Snapshot) projectSnapshotResponse {
	return projectSnapshotResponse{ProjectID: value.ProjectID, ProjectName: value.ProjectName, ContractNo: value.ContractNo, Status: value.Status, ProgressPct: value.ProgressPct, CurrentStage: value.CurrentStage, ExpectedEndDate: value.ExpectedEndDate, Delayed: value.Delayed, ManagerName: value.ManagerName, ManagerContactMasked: value.ManagerContactMasked, ManagerMessageAvailable: value.ManagerPortalAccountID != "", SourceUpdatedAt: value.SourceUpdatedAt, SyncedAt: value.SyncedAt}
}
func publicActivity(value *project.Activity) projectActivityResponse {
	return projectActivityResponse{Type: value.Type, Content: value.Content, OccurredAt: value.OccurredAt}
}
func publicProjectBundle(value *project.Detail) projectBundleResponse {
	result := projectBundleResponse{Snapshot: publicProjectSnapshot(&value.Snapshot), Milestones: make([]projectMilestoneResponse, 0, len(value.Milestones)), Activities: []projectActivityResponse{}, Team: make([]projectTeamResponse, 0, len(value.Team))}
	for _, item := range value.Milestones {
		result.Milestones = append(result.Milestones, projectMilestoneResponse{StageCode: item.StageCode, StageName: item.StageName, Status: item.Status, PlannedAt: item.PlannedAt, CompletedAt: item.CompletedAt, SortNo: item.SortNo})
	}
	for _, item := range value.Team {
		result.Team = append(result.Team, projectTeamResponse{Name: item.Name, Role: item.Role, ContactMasked: item.ContactMasked})
	}
	return result
}
