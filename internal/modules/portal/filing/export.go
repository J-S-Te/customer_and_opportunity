package filing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	requestctx "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/request"
)

var ErrExportUnavailable = apperror.New(http.StatusServiceUnavailable, "PORTAL_FILING_EXPORT_UNAVAILABLE", "备案 PDF 中文渲染依赖尚未配置")

type TextRenderer interface {
	RenderRows(context.Context, []string) ([]byte, error)
}
type ExportFile struct {
	Content          []byte
	FileName, SHA256 string
}

func (s *Service) UseExportRenderer(renderer TextRenderer) *Service {
	s.exports = renderer
	s.exportSlots = make(chan struct{}, 2)
	return s
}
func (s *Service) ExportAvailable() bool { return s != nil && s.exports != nil }

// Export 以客户/租户边界锁定备案版本，渲染七步表单与材料清单，并在返回文件前持久化
// 加密审计。导出仅为现有数据副本，不伪造公安受理回执，也不下载或拼接材料正文。
func (s *Service) Export(ctx context.Context, actor Actor, publicID string, expectedVersion uint64) (*ExportFile, error) {
	if !validActor(actor) || !validPublicID(publicID) || expectedVersion == 0 {
		return nil, ErrValidation
	}
	if !s.ExportAvailable() {
		return nil, ErrExportUnavailable
	}
	select {
	case s.exportSlots <- struct{}{}:
		defer func() { <-s.exportSlots }()
	default:
		return nil, apperror.New(http.StatusTooManyRequests, "PORTAL_FILING_EXPORT_BUSY", "备案 PDF 生成繁忙，请稍后重试")
	}
	var view *View
	var id uint64
	// 父行锁阻止表单与材料在捕获期间修改，明文只保存在本次请求内存中。
	if err := s.repo.WithTransaction(ctx, func(tx context.Context) error {
		value, err := s.repo.FindOwnedForUpdate(tx, actor, strings.TrimSpace(publicID))
		if err != nil {
			return err
		}
		if value.Version != expectedVersion {
			return ErrVersionConflict
		}
		id = value.ID
		view, err = s.view(tx, value)
		if err != nil {
			return err
		}
		if value.Status != StatusDraft {
			snapshot, readErr := s.repo.LatestSubmission(tx, actor.TenantID, value.ID)
			if readErr != nil {
				return readErr
			}
			plain, decryptErr := s.protector.Decrypt(tx, snapshot.CanonicalCipher)
			if decryptErr != nil {
				return decryptErr
			}
			digest := sha256.Sum256(plain)
			if hex.EncodeToString(digest[:]) != snapshot.SnapshotSHA256 {
				return ErrExportUnavailable
			}
			var document snapshotDocument
			if decodeErr := json.Unmarshal(plain, &document); decodeErr != nil {
				return decodeErr
			}
			if document.FormVersion != value.FormVersion {
				return ErrExportUnavailable
			}
			view.Sections, view.Matrices = nil, nil
			for _, section := range document.Sections {
				view.Sections = append(view.Sections, SectionView{Code: section.Code, SchemaVersion: section.SchemaVersion, Data: section.Data})
			}
			for _, matrix := range document.Matrices {
				view.Matrices = append(view.Matrices, MatrixView{Code: matrix.Code, RowCode: matrix.RowCode, ColumnCode: matrix.ColumnCode, Selected: true})
			}
		}
		return err
	}); err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	rows, err := exportRows(*view, now)
	if err != nil {
		return nil, err
	}
	content, err := s.exports.RenderRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > 2<<20 || !bytes.HasPrefix(content, []byte("%PDF-")) {
		return nil, ErrExportUnavailable
	}
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	// 内容摘要与捕获版本形成不可变证据；不把导出的敏感正文或账号写入明文日志。
	metadata, err := json.Marshal(map[string]any{"filing_no": view.FilingNo, "version": view.Version, "sha256": digest, "size": len(content)})
	if err != nil {
		return nil, err
	}
	cipher, err := s.protector.Encrypt(ctx, metadata)
	if err != nil {
		return nil, err
	}
	key := s.ids.NewID()
	if !validGeneratedID(key) {
		return nil, ErrExportUnavailable
	}
	if err := s.repo.CreateAction(ctx, &Action{TenantID: actor.TenantID, FilingID: id, Action: "EXPORT_PDF", ActorType: "CUSTOMER", ActorID: actor.AccountID, IdempotencyKey: key, RequestHash: hashValue(metadata), RequestCipher: cipher, ResponseCipher: cipher, RequestID: requestctx.ID(ctx), OccurredAt: now}); err != nil {
		return nil, err
	}
	return &ExportFile{Content: content, FileName: "filing-" + strconv.FormatUint(view.Version, 10) + ".pdf", SHA256: digest}, nil
}

func exportRows(view View, generatedAt time.Time) ([]string, error) {
	rows := []string{"等级保护备案资料副本", "本文件为已保存资料的导出，不等同于公安受理或备案凭证。", "备案编号：" + view.FilingNo, "版本：" + strconv.FormatUint(view.Version, 10), "状态：" + view.Status, "单位：" + view.UnitName, "系统：" + view.SystemName, "生成时间（UTC）：" + generatedAt.UTC().Format(time.RFC3339)}
	if view.Status == StatusDraft {
		rows = append(rows, "草稿：可能仍有未填写或未通过校验的内容。")
	}
	labels := map[string]string{"ORGANIZATION": "单位基本情况", "CLASSIFIED_OBJECT": "定级对象情况", "CLASSIFICATION": "定级情况", "NEW_TECHNOLOGY": "新技术应用", "DATA_INVENTORY": "数据摸底", "CLASSIFICATION_REPORT": "定级报告"}
	for _, section := range view.Sections {
		rows = append(rows, "", labels[section.Code]+"（"+section.Code+"）")
		var fields map[string]any
		decoder := json.NewDecoder(bytes.NewReader(section.Data))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := json.Marshal(fields[key])
			if err != nil {
				return nil, err
			}
			var text string
			if str, ok := fields[key].(string); ok {
				text = str
			} else {
				text = string(value)
			}
			label := exportFieldLabels[key]
			if label == "" {
				label = key
			}
			rows = append(rows, label+"："+text)
		}
	}
	rows = append(rows, "", "业务信息／系统服务矩阵：")
	for _, matrix := range view.Matrices {
		rows = append(rows, fmt.Sprintf("%s / %s / %s：%t", matrix.Code, matrix.RowCode, matrix.ColumnCode, matrix.Selected))
	}
	rows = append(rows, "", "提交材料清单（附件正文不包含在本文件中）：")
	for _, material := range view.Materials {
		rows = append(rows, fmt.Sprintf("%s：%s，%s，%d 字节，校验 %s", material.Code, material.FileName, material.MIMEType, material.SizeBytes, material.ScanStatus))
	}
	if view.Submission != nil {
		rows = append(rows, "提交序号："+strconv.FormatUint(view.Submission.Sequence, 10), "提交快照 SHA-256："+view.Submission.SnapshotSHA256)
	}
	return rows, nil
}

var exportFieldLabels = map[string]string{
	"unit_name": "单位名称", "social_credit_code": "统一社会信用代码", "province": "省（自治区、直辖市）", "city": "地（区、市、州、盟）", "district": "县（区、市、旗）", "address": "详细地址", "postal_code": "邮政编码", "administrative_division_code": "行政区划代码",
	"organization_leader_name": "单位负责人姓名", "organization_leader_title": "单位负责人职务/职称", "organization_leader_phone": "单位负责人电话", "organization_leader_email": "单位负责人邮箱", "security_department": "网络安全责任部门", "security_contact_name": "网络安全联系人", "security_contact_phone": "网络安全联系人电话", "security_contact_email": "网络安全联系人邮箱", "data_security_department": "数据安全管理部门", "data_security_contact_name": "数据安全联系人",
	"affiliation": "隶属关系", "organization_type": "单位类型", "industry_code": "行业类别代码", "level2_object_count": "第二级定级对象数", "level3_object_count": "第三级定级对象数", "level4_object_count": "第四级定级对象数", "level5_object_count": "第五级定级对象数",
	"system_name": "定级对象（系统名称）", "object_number": "定级对象编号", "object_types": "定级对象类型", "business_type": "业务类型", "business_description": "业务描述", "launched_on": "投入运行日期", "service_scope": "服务范围", "service_audience": "服务对象", "deployment_scope": "部署范围", "network_nature": "网络性质", "source_ip_range": "源站 IP 地址范围", "domain_name": "域名", "protocols_and_ports": "主要协议/端口", "interconnection": "网络互联情况", "is_subsystem": "是否是分系统", "parent_system_name": "上级系统名称", "parent_organization_name": "上级系统所属单位",
	"business_information_level": "业务信息安全保护等级", "system_service_level": "系统服务安全保护等级", "final_level": "定级对象安全保护等级", "classified_on": "定级时间", "classification_report_available": "已有定级报告声明", "expert_reviewed": "已通过专家评审", "has_industry_authority": "有上级行业主管部门", "industry_authority_name": "上级行业主管部门名称", "industry_authority_reviewed": "已通过上级行业主管部门审核", "form_filler_name": "填表人姓名", "form_filled_on": "填表日期",
	"cloud_used": "使用云计算技术", "cloud_responsibility": "云计算角色", "cloud_service_model": "云服务模式", "cloud_deployment_model": "云部署模式", "cloud_platform_name": "云平台名称", "cloud_platform_level": "云平台安全保护等级", "cloud_filing_number": "云平台备案编号", "mobile_used": "使用移动互联技术", "mobile_application_names": "移动应用名称", "iot_used": "使用物联网技术", "iot_components": "物联网组成", "industrial_control_used": "使用工业控制技术", "industrial_control_components": "工业控制组成", "big_data_used": "使用大数据技术", "big_data_components": "大数据组成", "big_data_cross_border": "大数据涉及跨境", "big_data_application_count": "大数据应用数量", "big_data_platform_name": "大数据平台名称",
	"topology_available": "网络拓扑图", "security_governance_available": "网络安全管理制度", "security_design_available": "网络安全建设方案", "security_products_available": "网络安全产品清单", "security_services_available": "网络安全服务清单", "authority_guidance_available": "上级主管部门指导意见",
	"data_name": "数据名称", "proposed_data_level": "拟定数据级别", "data_category": "数据类别", "responsible_department": "责任部门", "responsible_person": "责任人", "total_volume": "数据总量", "monthly_growth": "月增长量", "personal_information_types": "个人信息类型", "data_sources": "数据来源", "inter_organization_flow": "组织间流转情况", "processor_interaction": "处理者交互方式", "storage_locations": "存储位置",
	"responsible_entity_description": "定级责任主体说明", "object_composition_description": "定级对象构成说明", "subsystems_summary": "子系统概要", "data_description": "数据情况说明", "security_responsibility_description": "安全责任说明", "business_information_description": "业务信息受损影响说明", "business_impact_object": "业务信息受损侵害客体", "business_damage_degree": "业务信息受损程度", "system_service_description": "系统服务受损影响说明", "service_impact_object": "系统服务受损侵害客体", "service_damage_degree": "系统服务受损程度",
}
