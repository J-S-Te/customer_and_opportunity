package evaluation

import "time"

// View 刻意排除持久化主键、租户/客户/账号范围、审计操作者和幂等材料。
type View struct {
	ID                string    `json:"id"`
	EvaluationNo      string    `json:"evaluation_no"`
	ProjectID         string    `json:"project_id"`
	ProfessionalScore uint8     `json:"professional_score"`
	ResponseScore     uint8     `json:"response_score"`
	ReportScore       uint8     `json:"report_score"`
	AttitudeScore     uint8     `json:"attitude_score"`
	TotalScore        uint8     `json:"total_score"`
	AverageScore      string    `json:"average_score"`
	Comment           string    `json:"comment,omitempty"`
	Status            string    `json:"status"`
	SubmittedAt       time.Time `json:"submitted_at"`
}

type Eligibility struct {
	ProjectID    string `json:"project_id"`
	Eligible     bool   `json:"eligible"`
	ReasonCode   string `json:"reason_code"`
	EvaluationID string `json:"evaluation_id,omitempty"`
}

type Statistics struct {
	SampleSize          int64  `json:"sample_size"`
	ProfessionalAverage string `json:"professional_average"`
	ResponseAverage     string `json:"response_average"`
	ReportAverage       string `json:"report_average"`
	AttitudeAverage     string `json:"attitude_average"`
	OverallAverage      string `json:"overall_average"`
}

type LowScoreNotice struct {
	ID                string     `json:"id"`
	EvaluationNo      string     `json:"evaluation_no"`
	ProjectID         string     `json:"project_id"`
	ProfessionalScore uint8      `json:"professional_score"`
	ResponseScore     uint8      `json:"response_score"`
	ReportScore       uint8      `json:"report_score"`
	AttitudeScore     uint8      `json:"attitude_score"`
	AverageScore      string     `json:"average_score"`
	Comment           string     `json:"comment,omitempty"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	ReadAt            *time.Time `json:"read_at,omitempty"`
}

func publicView(value *ServiceEvaluation) View {
	return View{
		ID: value.PublicID, EvaluationNo: value.EvaluationNo, ProjectID: value.ProjectID,
		ProfessionalScore: value.ProfessionalScore, ResponseScore: value.ResponseScore,
		ReportScore: value.ReportScore, AttitudeScore: value.AttitudeScore,
		TotalScore: value.TotalScore, AverageScore: value.AverageScore,
		Comment: value.Comment, Status: value.Status, SubmittedAt: value.SubmittedAt,
	}
}
