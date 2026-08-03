package projectexportworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/projectexport"
)

const maxPDFBytes = 2 << 20

type Store interface {
	Claim(context.Context, string, time.Time, time.Duration) (*projectexport.Job, error)
	Complete(context.Context, *projectexport.Job, string, string, []byte, string, time.Time) error
	Fail(context.Context, *projectexport.Job, string, string, time.Time) error
}

type Renderer interface {
	Render(context.Context, project.Detail, time.Time) ([]byte, error)
}

type Worker struct {
	store                       Store
	renderer                    Renderer
	workerID                    string
	pollInterval, leaseDuration time.Duration
	now                         func() time.Time
}

func NewWorker(store Store, renderer Renderer, workerID string, pollInterval, leaseDuration time.Duration) *Worker {
	return &Worker{store: store, renderer: renderer, workerID: workerID, pollInterval: pollInterval, leaseDuration: leaseDuration, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		_, err := w.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now().UTC()
	job, err := w.store.Claim(ctx, w.workerID, now, w.leaseDuration)
	if err != nil || job == nil {
		return false, err
	}
	var capture projectexport.Capture
	// 导出只使用创建任务时冻结的快照，并复核租户、客户、项目和源版本绑定，绝不在渲染时读取漂移中的实时数据。
	if err = json.Unmarshal(job.SnapshotJSON, &capture); err != nil || capture.TenantID != job.TenantID || capture.CustomerID != job.CustomerID || capture.Detail.Snapshot.ProjectID != job.ProjectID || capture.Detail.Snapshot.CustomerID != job.CustomerID || !capture.Detail.Snapshot.SourceUpdatedAt.Equal(job.SourceUpdatedAt) {
		failErr := w.store.Fail(ctx, job, w.workerID, "PORTAL_PROJECT_EXPORT_SNAPSHOT_INVALID", w.now().UTC())
		return true, errors.Join(err, failErr)
	}
	detail := capture.Detail
	pdf, err := w.renderer.Render(ctx, detail, now)
	// 即使渲染器未报错，也校验 PDF 魔数和大小上限，防止错误格式或异常大对象进入数据库下载链路。
	if err != nil || len(pdf) == 0 || len(pdf) > maxPDFBytes || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		failErr := w.store.Fail(ctx, job, w.workerID, "PORTAL_PROJECT_EXPORT_RENDER_FAILED", w.now().UTC())
		return true, errors.Join(err, failErr)
	}
	hash := sha256.Sum256(pdf)
	fileName := safeFileName(detail.Snapshot.ProjectName)
	return true, w.store.Complete(ctx, job, w.workerID, fileName, pdf, hex.EncodeToString(hash[:]), w.now().UTC())
}

func safeFileName(projectName string) string {
	value := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(projectName))
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	if value == "" || value == "." || value == ".." {
		value = "project-progress"
	}
	return value + "-项目进度.pdf"
}

// PDFCPURenderer 从不可变快照生成真实 PDF；字体路径属于显式部署配置，不依赖宿主机隐式环境。
type PDFCPURenderer struct {
	config   *model.Configuration
	fontName string
}

func NewPDFCPURenderer(configRoot, fontPath string) (*PDFCPURenderer, error) {
	configRoot, fontPath = strings.TrimSpace(configRoot), strings.TrimSpace(fontPath)
	if configRoot == "" || fontPath == "" {
		return nil, errors.New("PDF config root and CJK font path are required")
	}
	if err := api.EnsureDefaultConfigAt(configRoot); err != nil {
		return nil, err
	}
	fontName := strings.TrimSuffix(path.Base(fontPath), path.Ext(fontPath))
	if err := api.InstallFonts([]string{fontPath}); err != nil {
		return nil, err
	}
	return &PDFCPURenderer{config: api.LoadConfiguration(), fontName: fontName}, nil
}

func (r *PDFCPURenderer) Render(ctx context.Context, detail project.Detail, generatedAt time.Time) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows := []string{
		"项目名称：" + detail.Snapshot.ProjectName,
		"项目编号：" + detail.Snapshot.ProjectID,
		"合同编号：" + emptyDash(detail.Snapshot.ContractNo),
		fmt.Sprintf("总体进度：%d%%", detail.Snapshot.ProgressPct),
		"当前阶段：" + detail.Snapshot.CurrentStage,
		"项目状态：" + detail.Snapshot.Status,
		"预计结束：" + formatDate(detail.Snapshot.ExpectedEndDate),
		"延期状态：" + map[bool]string{true: "已延期", false: "未标记延期"}[detail.Snapshot.Delayed],
		"项目经理：" + emptyDash(detail.Snapshot.ManagerName),
		"受控联系方式：" + emptyDash(detail.Snapshot.ManagerContactMasked),
		"数据更新时间：" + detail.Snapshot.SourceUpdatedAt.UTC().Format(time.RFC3339),
		"PDF 生成时间：" + generatedAt.UTC().Format(time.RFC3339),
		"",
		"里程碑：",
	}
	for _, item := range detail.Milestones {
		rows = append(rows, fmt.Sprintf("%d. %s（%s）计划 %s，完成 %s", item.SortNo, emptyDash(item.StageName), emptyDash(item.Status), formatDate(item.PlannedAt), formatDate(item.CompletedAt)))
	}
	rows = append(rows, "", "项目团队：")
	for _, item := range detail.Team {
		rows = append(rows, fmt.Sprintf("%s / %s / %s", emptyDash(item.Name), emptyDash(item.Role), emptyDash(item.ContactMasked)))
	}
	pageLines := paginatePDFRows(rows, 52, 44)
	pages := make(map[string]any, len(pageLines))
	for index, lines := range pageLines {
		pages[strconv.Itoa(index+1)] = map[string]any{"content": map[string]any{"text": []any{map[string]any{
			"value": strings.Join(lines, "\n"), "pos": []float64{28, 800}, "width": 539,
			"font": map[string]any{"name": "$zh", "size": 11}, "lheight": 15,
		}}}}
	}
	doc := map[string]any{
		"paper": "A4P", "crop": "10", "origin": "LowerLeft", "contentBox": true,
		"fonts":  map[string]any{"zh": map[string]any{"name": r.fontName, "lang": "zh", "script": "HanS", "size": 11}},
		"margin": map[string]any{"width": 28},
		"pages":  pages,
	}
	input, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err = api.Create(nil, bytes.NewReader(input), &output, r.config); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func paginatePDFRows(rows []string, columns, linesPerPage int) [][]string {
	if columns < 1 || linesPerPage < 1 {
		return nil
	}
	wrapped := make([]string, 0, len(rows))
	for _, row := range rows {
		wrapped = append(wrapped, wrapPDFLine(row, columns)...)
	}
	if len(wrapped) == 0 {
		wrapped = append(wrapped, "")
	}
	pages := make([][]string, 0, (len(wrapped)+linesPerPage-1)/linesPerPage)
	for len(wrapped) > 0 {
		count := linesPerPage
		if len(wrapped) < count {
			count = len(wrapped)
		}
		pages = append(pages, append([]string(nil), wrapped[:count]...))
		wrapped = wrapped[count:]
	}
	return pages
}

func wrapPDFLine(value string, columns int) []string {
	// 使用显示宽度而非 rune 数量换行，保证中英文混排在等宽 PDF 文本布局中不会越界。
	if value == "" {
		return []string{""}
	}
	result := make([]string, 0, 1)
	var line strings.Builder
	width := 0
	for _, char := range value {
		charWidth := runewidth.RuneWidth(char)
		if charWidth < 1 {
			charWidth = 1
		}
		if width > 0 && width+charWidth > columns {
			result = append(result, line.String())
			line.Reset()
			width = 0
		}
		line.WriteRune(char)
		width += charWidth
	}
	result = append(result, line.String())
	return result
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return strings.TrimSpace(value)
}
func formatDate(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.UTC().Format("2006-01-02")
}
