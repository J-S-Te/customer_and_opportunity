// Package seeddemo 提供可重复执行的客户与商机演示数据。人员、客户和商机沿用原型与验收用例
// 中的稳定身份和业务名称，使本地 UAT 页面在多次初始化后仍可识别。
package seeddemo

const (
	DefaultTenantID  = "tenant-demo"
	DefaultActorID   = "01KYDVHC00000000000000000C"
	DefaultActorName = "张伟"
)

// Person 表示内部演示操作者：Sub 是平台 OIDC subject，OrgID 必须属于该主体的有效组织任职；
// 所有者目录会对这两个值成对校验。
type Person struct {
	Sub     string
	Name    string
	OrgID   string
	OrgName string
}

// 演示所有者以稳定键引用，不用可变显示名称建立关系。
func People() map[string]Person {
	return map[string]Person{
		"张伟": {Sub: "01KYDVHC00000000000000000C", Name: "张伟", OrgID: "01KYDVHC000000000000000002", OrgName: "平台研发部"},
		"李娜": {Sub: "01KYDVHC00000000000000000D", Name: "李娜", OrgID: "01KYDVHC000000000000000002", OrgName: "平台研发部"},
		"王强": {Sub: "01KYDVHC00000000000000000E", Name: "王强", OrgID: "01KYDVHC000000000000000003", OrgName: "运维保障部"},
		"陈晨": {Sub: "01KYDVHC00000000000000000F", Name: "陈晨", OrgID: "01KYDVHC000000000000000005", OrgName: "产品部"},
		"刘洋": {Sub: "01KYDVHC00000000000000000G", Name: "刘洋", OrgID: "01KYDVHC000000000000000006", OrgName: "客户成功部"},
	}
}

type contactSeed struct {
	Name         string
	Phone        string
	Email        string
	Registration bool
}

type stakeholderSeed struct {
	Name                string
	RoleTitle           string
	Influence           string
	RelationshipSummary string
	Phone               string
	Email               string
}

type systemSeed struct {
	Name                string
	ProtectionLevel     string
	ApplicationScenario string
	FilingNo            string
	FilingStatus        string
	GradingDate         string
}

type followupSeed struct {
	Type         string
	Content      string
	FollowedAt   string
	NextFollowAt string
}

type customerSeed struct {
	Key               string
	Name              string
	UnifiedCreditCode string
	CustomerType      string
	Industry          string
	Region            string
	OwnerKey          string
	Contacts          []contactSeed
	Stakeholders      []stakeholderSeed
	Systems           []systemSeed
	Followups         []followupSeed
}

type terminalSeed struct {
	Stage       string
	FromStage   string
	PendingType string
	ContractRef string
	LostReason  string
}

type memberSeed struct {
	UserKey string
	Role    string
}

type opportunitySeed struct {
	Key                string
	Name               string
	CustomerKey        string
	Type               string
	Source             string
	ExpectedAmount     string
	ExpectedSignDate   string
	RequirementSummary string
	SystemCount        uint32
	PainPoints         string
	CompetitorInfo     string
	OwnerKey           string
	Stage              string
	Terminal           *terminalSeed
	Members            []memberSeed
	Followups          []followupSeed
}

func customers() []customerSeed {
	return []customerSeed{
		{
			Key: "huaxing", Name: "华兴证券股份有限公司", UnifiedCreditCode: "91440300100008888K",
			CustomerType: "业主", Industry: "金融", Region: "华南", OwnerKey: "张伟",
			Contacts: []contactSeed{
				{Name: "陈志远", Phone: "13800136789", Email: "chenzhiyuan@huaxing.example.com", Registration: true},
				{Name: "王芳", Phone: "13900136990", Email: "wangfang@huaxing.example.com"},
			},
			Stakeholders: []stakeholderSeed{
				{Name: "陈志远", RoleTitle: "信息技术部总监", Influence: "HIGH", RelationshipSummary: "等保测评与安全服务采购决策人", Phone: "13800136789", Email: "chenzhiyuan@huaxing.example.com"},
				{Name: "王芳", RoleTitle: "采购部经理", Influence: "MEDIUM", RelationshipSummary: "招标与商务流程执行人", Phone: "13900136990", Email: "wangfang@huaxing.example.com"},
			},
			Systems: []systemSeed{
				{Name: "证券集中交易系统", ProtectionLevel: "LEVEL_3", ApplicationScenario: "证券交易", FilingNo: "440300-00001", FilingStatus: "FILED", GradingDate: "2025-06-30"},
				{Name: "网上证券交易系统", ProtectionLevel: "LEVEL_3", ApplicationScenario: "互联网交易", FilingNo: "440300-00002", FilingStatus: "FILED", GradingDate: "2025-06-30"},
			},
			Followups: []followupSeed{
				{Type: "PHONE", Content: "沟通等保测评范围与报价预期，客户要求两周内给出分阶段方案。", FollowedAt: "2026-07-28T10:30:00Z", NextFollowAt: "2026-08-05T10:00:00Z"},
			},
		},
		{
			Key: "pengcheng", Name: "鹏程能源科技有限公司", UnifiedCreditCode: "91440100000012345X",
			CustomerType: "三方", Industry: "能源", Region: "华南", OwnerKey: "李娜",
			Contacts: []contactSeed{
				{Name: "李明", Phone: "13800136890", Email: "liming@pengcheng.example.com", Registration: true},
			},
			Stakeholders: []stakeholderSeed{
				{Name: "李明", RoleTitle: "信息中心主任", Influence: "HIGH", RelationshipSummary: "风险评估需求发起与接口人", Phone: "13800136890", Email: "liming@pengcheng.example.com"},
			},
			Systems: []systemSeed{
				{Name: "新能源集控系统", ProtectionLevel: "LEVEL_3", ApplicationScenario: "生产控制", FilingNo: "440100-00001", FilingStatus: "FILED", GradingDate: "2025-09-30"},
			},
			Followups: []followupSeed{
				{Type: "VISIT", Content: "现场拜访并了解公开招标文件要求。", FollowedAt: "2026-07-25T09:00:00Z", NextFollowAt: "2026-08-10T14:00:00Z"},
			},
		},
		{
			Key: "binjiang", Name: "滨江医疗集团有限公司", UnifiedCreditCode: "91330000000067890Y",
			CustomerType: "政府", Industry: "医疗", Region: "华东", OwnerKey: "张伟",
			Contacts: []contactSeed{
				{Name: "刘主任", Phone: "13700136788", Email: "liuzhuren@binjiang.example.com", Registration: true},
			},
			Stakeholders: []stakeholderSeed{
				{Name: "刘主任", RoleTitle: "信息科负责人", Influence: "HIGH", RelationshipSummary: "医疗信息平台项目接口人", Phone: "13700136788", Email: "liuzhuren@binjiang.example.com"},
			},
			Systems: []systemSeed{
				{Name: "医院信息平台", ProtectionLevel: "LEVEL_3", ApplicationScenario: "医疗信息", FilingNo: "330100-00001", FilingStatus: "FILED", GradingDate: "2025-12-20"},
			},
			Followups: []followupSeed{
				{Type: "PHONE", Content: "确认等保测评需求与预算审批节奏。", FollowedAt: "2026-07-20T15:00:00Z", NextFollowAt: "2026-08-12T10:00:00Z"},
			},
		},
		{
			Key: "zhongcheng", Name: "中诚商业银行股份有限公司", UnifiedCreditCode: "91110000123456789X",
			CustomerType: "业主", Industry: "金融", Region: "华北", OwnerKey: "张伟",
			Contacts: []contactSeed{
				{Name: "李经理", Phone: "13800112233", Email: "lim@zhongcheng.example.com", Registration: true},
				{Name: "张总", Phone: "13800112244", Email: "zhang@zhongcheng.example.com"},
			},
			Stakeholders: []stakeholderSeed{
				{Name: "李经理", RoleTitle: "信息技术部经理", Influence: "HIGH", RelationshipSummary: "需求发起与实施接口人", Phone: "13800112233", Email: "lim@zhongcheng.example.com"},
				{Name: "张总", RoleTitle: "分管副行长", Influence: "HIGH", RelationshipSummary: "最终预算与签约决策人", Phone: "13800112244", Email: "zhang@zhongcheng.example.com"},
			},
			Systems: []systemSeed{
				{Name: "核心交易系统", ProtectionLevel: "LEVEL_4", ApplicationScenario: "银行交易", FilingNo: "110100-00001", FilingStatus: "FILED", GradingDate: "2025-03-31"},
				{Name: "网上银行系统", ProtectionLevel: "LEVEL_3", ApplicationScenario: "互联网金融服务", FilingNo: "110100-00002", FilingStatus: "FILED", GradingDate: "2025-03-31"},
			},
			Followups: []followupSeed{
				{Type: "VISIT", Content: "现场调研核心交易系统现状，完成等保三级初步差距分析。", FollowedAt: "2026-07-30T09:30:00Z", NextFollowAt: "2026-08-06T14:00:00Z"},
			},
		},
	}
}

func opportunities() []opportunitySeed {
	return []opportunitySeed{
		{
			Key: "bank-core-etc", Name: "核心交易系统等保测评", CustomerKey: "zhongcheng",
			Type: "新购", Source: "线索", ExpectedAmount: "45.80", ExpectedSignDate: "2026-09-30",
			RequirementSummary: "客户核心交易系统拟做等保三级测评，需出具技术方案与初步差距分析。",
			SystemCount:        2, PainPoints: "对测评周期和整改成本敏感，需要分阶段实施路径。",
			CompetitorInfo: "中诚安全测评有限公司报价偏低，但服务覆盖范围较窄。",
			OwnerKey:       "张伟", Stage: "需求沟通",
			Members: []memberSeed{
				{UserKey: "陈晨", Role: "TECHNICAL_SUPPORT"},
				{UserKey: "刘洋", Role: "BUSINESS_SUPPORT"},
				{UserKey: "李娜", Role: "SALES_SUPPORT"},
			},
			Followups: []followupSeed{
				{Type: "PHONE", Content: "确认需求文档与现场调研时间。", FollowedAt: "2026-07-29T11:00:00Z", NextFollowAt: "2026-08-06T10:00:00Z"},
			},
		},
		{
			Key: "bank-online-etc", Name: "网上交易系统等保测评", CustomerKey: "zhongcheng",
			Type: "新购", Source: "自主开发", ExpectedAmount: "62.00", ExpectedSignDate: "2026-10-15",
			RequirementSummary: "网上银行系统按监管要求完成等保测评与整改复测。",
			SystemCount:        2, PainPoints: "互联网暴露面大，需要兼顾业务连续性。",
			OwnerKey: "李娜", Stage: "方案制定",
		},
		{
			Key: "binjiang-platform", Name: "医院信息平台安全建设", CustomerKey: "binjiang",
			Type: "新购", Source: "自主开发", ExpectedAmount: "85.20", ExpectedSignDate: "2026-09-10",
			RequirementSummary: "医院信息平台整体安全建设，含等保测评、边界防护与安全运营。",
			SystemCount:        3, PainPoints: "门诊高峰时段业务不能中断，整改窗口有限。",
			CompetitorInfo: "安恒信息技术有限公司品牌知名度高，但报价偏高。",
			OwnerKey:       "陈晨", Stage: "方案制定",
		},
		{
			Key: "huaxing-core-etc", Name: "证券核心系统等保测评", CustomerKey: "huaxing",
			Type: "新购", Source: "老客户转介绍", ExpectedAmount: "128.00", ExpectedSignDate: "2026-08-30",
			RequirementSummary: "证券集中交易系统等保三级测评、渗透测试与整改协助。",
			SystemCount:        2, PainPoints: "交易时段保护要求高，测试窗口受交易所休市限制。",
			CompetitorInfo: "绿盟科技股份有限公司进入二轮比价。",
			OwnerKey:       "张伟", Stage: "报价",
			Members: []memberSeed{
				{UserKey: "陈晨", Role: "TECHNICAL_SUPPORT"},
				{UserKey: "刘洋", Role: "TECHNICAL_SUPPORT"},
			},
			Followups: []followupSeed{
				{Type: "PHONE", Content: "发送正式报价单，等待客户内部评审。", FollowedAt: "2026-07-28T16:00:00Z", NextFollowAt: "2026-08-05T15:00:00Z"},
			},
		},
		{
			Key: "pengcheng-scada", Name: "新能源集控系统风险评估", CustomerKey: "pengcheng",
			Type: "新购", Source: "公开招标", ExpectedAmount: "96.50", ExpectedSignDate: "2026-11-20",
			RequirementSummary: "新能源集控系统风险评估与安全加固，按公开招标文件执行。",
			SystemCount:        1, PainPoints: "招标文件对资质与同类案例要求严格。",
			CompetitorInfo: "两家本地测评机构参与投标。",
			OwnerKey:       "王强", Stage: "投标",
			Members: []memberSeed{
				{UserKey: "刘洋", Role: "TECHNICAL_SUPPORT"},
				{UserKey: "陈晨", Role: "TECHNICAL_SUPPORT"},
				{UserKey: "张伟", Role: "SALES_SUPPORT"},
			},
			Followups: []followupSeed{
				{Type: "VISIT", Content: "现场踏勘并完成招标答疑。", FollowedAt: "2026-07-25T10:00:00Z", NextFollowAt: "2026-08-10T09:00:00Z"},
			},
		},
		{
			Key: "huaxing-pentest", Name: "经纪业务系统渗透测试", CustomerKey: "huaxing",
			Type: "新购", Source: "线索", ExpectedAmount: "32.00", ExpectedSignDate: "2026-12-31",
			RequirementSummary: "经纪业务系统上线前渗透测试与代码审计。",
			SystemCount:        2, PainPoints: "系统迭代频繁，测试范围需按版本锁定。",
			OwnerKey: "刘洋", Stage: "初步接触",
		},
		{
			Key: "binjiang-healthcloud", Name: "健康云平台安全建设", CustomerKey: "binjiang",
			Type: "新购", Source: "渠道合作", ExpectedAmount: "55.00", ExpectedSignDate: "2026-10-31",
			RequirementSummary: "健康云平台等保测评与数据安全体系建设。",
			SystemCount:        2, PainPoints: "涉及个人健康数据，隐私合规要求高。",
			OwnerKey: "张伟", Stage: "报价",
		},
		{
			Key: "huaxing-advisory", Name: "数据安全合规咨询", CustomerKey: "huaxing",
			Type: "服务", Source: "老客户转介绍", ExpectedAmount: "18.80", ExpectedSignDate: "2026-07-15",
			RequirementSummary: "数据安全分级分类与合规差距咨询。",
			SystemCount:        0, OwnerKey: "李娜", Stage: "已签约",
			Terminal: &terminalSeed{Stage: "已签约", FromStage: "报价", PendingType: "NONE", ContractRef: "HT-2026-0088"},
		},
		{
			Key: "pengcheng-legacy", Name: "老旧系统改造等保测评", CustomerKey: "pengcheng",
			Type: "新购", Source: "公开招标", ExpectedAmount: "40.00", ExpectedSignDate: "2026-06-30",
			RequirementSummary: "老旧工业控制系统改造后的等保测评。",
			SystemCount:        1, OwnerKey: "王强", Stage: "失败",
			Terminal: &terminalSeed{Stage: "失败", FromStage: "投标", PendingType: "NONE", LostReason: "预算不足"},
		},
		{
			Key: "zhongcheng-cloud", Name: "云平台迁移安全评估", CustomerKey: "zhongcheng",
			Type: "服务", Source: "自主开发", ExpectedAmount: "26.50", ExpectedSignDate: "2026-08-20",
			RequirementSummary: "核心系统云迁移前安全评估与迁移方案评审。",
			SystemCount:        2, OwnerKey: "张伟", Stage: "已签约",
			Terminal: &terminalSeed{Stage: "已签约", FromStage: "报价", PendingType: "CONTRACT"},
		},
		{
			Key: "binjiang-audit", Name: "内网安全基线审计", CustomerKey: "binjiang",
			Type: "服务", Source: "线索", ExpectedAmount: "15.00", ExpectedSignDate: "2026-09-30",
			RequirementSummary: "院内网安全基线核查与整改建议。",
			SystemCount:        1, OwnerKey: "刘洋", Stage: "失败",
			Terminal: &terminalSeed{Stage: "失败", FromStage: "报价", PendingType: "LOST_REASON"},
		},
	}
}
