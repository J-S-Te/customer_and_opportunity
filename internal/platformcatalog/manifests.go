package platformcatalog

// CRM 目录描述浏览器用户在客户、商机及内嵌售前模块中的权限。仅供系统集成的 scope 应注册在
// 专用 OAuth 客户端上，不能授予这些人员角色。
func CRMManifest() Manifest {
	permissions := []Permission{
		permission("customer.read", "查看客户", "customer", "read", "LOW"),
		// 客户全量查看是只读放开：持有它才能在严格数据范围之上阅读租户内全部客户，
		// 且只允许平台超级管理员授予，业务侧不能自行下发。
		superAdminOnly(permission("customer.read.all", "查看全部客户", "customer", "read_all", "HIGH")),
		permission("customer.create", "创建客户", "customer", "create", "MEDIUM"),
		permission("customer.update", "维护客户", "customer", "update", "MEDIUM"),
		permission("customer.duplicate.override", "覆盖客户疑似重名", "customer", "duplicate_override", "HIGH"),
		permission("customer.import", "导入客户", "customer", "import", "HIGH"),
		permission("customer.merge", "合并客户", "customer", "merge", "HIGH"),
		permission("customer.void", "作废客户", "customer", "void", "HIGH"),
		permission("customer.restore", "恢复客户", "customer", "restore", "HIGH"),
		permission("customer.export", "导出客户", "customer", "export", "HIGH"),
		permission("customer.audit.read", "查看客户审计", "customer_audit", "read", "HIGH"),
		permission("customer.credit.read", "查看客户信用等级", "customer_credit", "read", "LOW"),
		permission("customer.credit.apply", "申请调整客户信用等级", "customer_credit", "apply", "HIGH"),
		permission("customer.credit.approve", "审批客户信用等级调整", "customer_credit", "approve", "HIGH"),
		permission("customer.credit.rule.manage", "管理客户信用规则", "customer_credit_rule", "manage", "CRITICAL"),
		permission("customer.credit.payment.ingest", "接收结算回款信用事件", "customer_credit", "ingest", "CRITICAL"),
		permission("customer.credit.internal.read", "内部查询客户信用等级", "customer_credit", "internal_read", "HIGH"),
		permission("opportunity.read", "查看商机", "opportunity", "read", "LOW"),
		permission("opportunity.create", "创建商机", "opportunity", "create", "MEDIUM"),
		permission("opportunity.update", "维护商机", "opportunity", "update", "MEDIUM"),
		permission("opportunity.owner.change", "变更商机负责人", "opportunity", "owner_change", "HIGH"),
		permission("opportunity.team.manage", "维护商机团队", "opportunity", "team_manage", "HIGH"),
		permission("opportunity.stage.change", "调整商机阶段", "opportunity", "stage_change", "HIGH"),
		permission("opportunity.contract.transfer", "推送商机签约事件", "opportunity", "contract_transfer", "HIGH"),
		permission("opportunity.attachment.read", "查看商机附件元数据", "opportunity_attachment", "read", "LOW"),
		permission("opportunity.attachment.upload", "上传商机附件", "opportunity_attachment", "upload", "MEDIUM"),
		permission("opportunity.attachment.download", "下载已通过扫描的商机附件", "opportunity_attachment", "download", "HIGH"),
		permission("opportunity.void", "作废商机", "opportunity", "void", "HIGH"),
		permission("opportunity.restore", "恢复商机", "opportunity", "restore", "HIGH"),
		permission("opportunity.alert.config", "配置商机阶段预警", "opportunity_alert", "configure", "HIGH"),
		permission("opportunity.catalog.manage", "管理商机类型与来源", "opportunity_catalog", "manage", "HIGH"),
		permission("portal_account.provision", "开通客户门户", "portal_account", "provision", "HIGH"),
		permission("portal_account.revoke", "撤销客户门户邀请", "portal_account", "revoke", "HIGH"),
		permission("portal_account.disable", "禁用客户门户访问", "portal_account", "disable", "HIGH"),
		permission("presale.read", "查看售前任务", "presale", "read", "LOW"),
		permission("presale.contact_phone.read", "查看售前联系电话明文", "presale_contact_phone", "read", "CRITICAL"),
		permission("presale.create", "发起售前申请", "presale", "create", "MEDIUM"),
		permission("presale.approve", "审批售前申请", "presale", "approve", "HIGH"),
		permission("presale.approval_rule.manage", "管理售前审批规则", "presale_approval_rule", "manage", "CRITICAL"),
		permission("presale.assign", "指派售前人员", "presale", "assign", "HIGH"),
		permission("presale.progress", "登记售前进度", "presale", "progress", "MEDIUM"),
		permission("presale.worklog", "登记售前工时", "presale", "worklog", "MEDIUM"),
		permission("presale.complete", "结束售前任务", "presale", "complete", "HIGH"),
		permission("presale.worklog.retry", "重试工时推送", "presale", "worklog_retry", "HIGH"),
		permission("presale.cancel", "取消售前申请", "presale", "cancel", "HIGH"),
		permission("presale.engineer.sync", "同步售前人员池", "presale_engineer", "synchronize", "HIGH"),
		permission("presale.alert.config", "配置售前超时预警", "presale_alert", "configure", "HIGH"),
		permission("presale.report", "查看售前投入报表", "presale_report", "read", "HIGH"),
	}
	roles := []Role{
		role("sales", "销售人员", "维护本人负责的客户和商机并发起售前申请",
			"customer.read", "customer.create", "customer.update", "customer.void", "customer.restore", "customer.credit.read", "customer.credit.apply",
			"opportunity.read", "opportunity.create", "opportunity.update", "opportunity.owner.change", "opportunity.team.manage", "opportunity.stage.change", "opportunity.attachment.read", "opportunity.attachment.upload", "opportunity.attachment.download", "opportunity.void", "opportunity.restore",
			"presale.read", "presale.create", "portal_account.provision", "portal_account.revoke", "portal_account.disable"),
		role("sales_director", "销售总监", "管理客户商机并执行售前一级审批",
			"customer.read", "customer.create", "customer.update", "customer.duplicate.override", "customer.import", "customer.merge", "customer.void", "customer.restore", "customer.export", "customer.audit.read", "customer.credit.read", "customer.credit.apply", "customer.credit.approve",
			"opportunity.read", "opportunity.create", "opportunity.update", "opportunity.owner.change", "opportunity.team.manage", "opportunity.stage.change", "opportunity.contract.transfer", "opportunity.attachment.read", "opportunity.attachment.upload", "opportunity.attachment.download", "opportunity.void", "opportunity.restore", "opportunity.alert.config", "opportunity.catalog.manage",
			"presale.read", "presale.contact_phone.read", "presale.create", "presale.approve", "presale.report", "portal_account.provision", "portal_account.revoke", "portal_account.disable"),
		role("technical_director", "技术总监", "执行售前技术审批并选择执行部门",
			"customer.read", "customer.void", "customer.restore", "opportunity.read", "presale.read", "presale.contact_phone.read", "presale.approve", "presale.assign", "presale.engineer.sync", "presale.alert.config", "presale.report"),
		role("team_lead", "团队负责人", "执行售前审批、人员指派和过程管理",
			"customer.read", "customer.void", "customer.restore", "opportunity.read", "presale.read", "presale.contact_phone.read", "presale.approve", "presale.assign", "presale.progress", "presale.worklog", "presale.complete", "presale.cancel", "presale.engineer.sync", "presale.alert.config", "presale.report"),
		role("technician", "技术人员", "仅处理本人被指派的售前任务", "presale.read", "presale.progress", "presale.worklog", "presale.complete"),
		role("project_manager", "项目经理", "管理本人被指派的售前执行任务", "presale.read", "presale.contact_phone.read", "presale.progress", "presale.worklog", "presale.complete"),
		role("customer_admin", "客户管理员", "执行客户主数据高风险维护",
			"customer.read", "customer.create", "customer.update", "customer.duplicate.override", "customer.import", "customer.merge", "customer.void", "customer.restore", "customer.export", "customer.audit.read", "customer.credit.read", "customer.credit.apply", "customer.credit.rule.manage",
			"opportunity.read", "opportunity.attachment.read", "portal_account.provision", "portal_account.revoke", "portal_account.disable"),
		role("crm_super_admin", "客户与商机超级管理员", "管理客户、商机、售前审批及审计配置",
			"customer.read", "customer.create", "customer.update", "customer.duplicate.override", "customer.import", "customer.merge", "customer.void", "customer.restore", "customer.export", "customer.audit.read", "customer.credit.read", "customer.credit.apply", "customer.credit.approve", "customer.credit.rule.manage",
			"opportunity.read", "opportunity.create", "opportunity.update", "opportunity.owner.change", "opportunity.team.manage", "opportunity.stage.change", "opportunity.contract.transfer", "opportunity.attachment.read", "opportunity.attachment.upload", "opportunity.attachment.download", "opportunity.void", "opportunity.restore", "opportunity.alert.config", "opportunity.catalog.manage",
			"portal_account.provision", "portal_account.revoke", "portal_account.disable",
			"presale.read", "presale.contact_phone.read", "presale.create", "presale.approve", "presale.approval_rule.manage", "presale.assign", "presale.progress", "presale.worklog", "presale.worklog.retry", "presale.cancel", "presale.engineer.sync", "presale.alert.config", "presale.report"),
		role("auditor", "审计员", "只读查看经营数据和审计记录", "customer.read", "customer.audit.read", "customer.credit.read", "opportunity.read", "opportunity.attachment.read", "presale.read", "presale.report"),
		// 全量查看员只放大客户读取范围：写路径仍按 ApplyDataScope 严格收窄，
		// 因此该角色不携带任何 customer 写权限。
		role("customer_all_viewer", "客户全量查看员", "查看租户内全部客户资料（只读）；仅平台超级管理员可授予",
			"customer.read", "customer.read.all"),
	}
	return Manifest{Version: "crm-2026.09.21-v12", Permissions: permissions, Roles: roles, Policy: Policy{MaxEffectiveRoles: 10}}
}

// PortalManifest 返回门户独立授权目录，管理权限仅授予真实门户超管。
// 参数：无。
// 返回值：浏览器权限、角色与单角色策略，客户数据边界仍由业务复核。
func PortalManifest() Manifest {
	permissions := []Permission{
		permission("project.read", "查看本人项目", "project", "read", "LOW"),
		permission("project.export", "导出本人项目", "project", "export", "HIGH"),
		permission("project.message.read", "查看本人项目站内会话", "project_message", "read", "LOW"),
		permission("project.message.send", "联系本人项目经理", "project_message", "send", "MEDIUM"),
		permission("report.request", "申请电子报告", "report", "request", "MEDIUM"),
		permission("report.read", "查看本人报告", "report", "read", "LOW"),
		permission("report.download", "下载本人报告", "report", "download", "HIGH"),
		permission("filing.read", "查看本人备案", "filing", "read", "LOW"),
		permission("filing.create", "创建备案", "filing", "create", "MEDIUM"),
		permission("filing.update", "维护备案草稿", "filing", "update", "MEDIUM"),
		permission("filing.submit", "提交备案", "filing", "submit", "HIGH"),
		permission("filing.delete", "删除备案草稿", "filing", "delete", "HIGH"),
		permission("evaluation.read", "查看本人服务评价", "evaluation", "read", "LOW"),
		permission("evaluation.create", "提交服务评价", "evaluation", "create", "MEDIUM"),
		permission("feedback.read", "查看本人反馈", "feedback", "read", "LOW"),
		permission("feedback.create", "提交客户反馈", "feedback", "create", "MEDIUM"),
		permission("feedback.reply", "回复本人反馈", "feedback", "reply", "MEDIUM"),
		permission("account.security.manage", "管理当前账号安全", "account_security", "manage", "HIGH"),
	}
	customerPermissions := permissionCodes(permissions)
	permissions = append(permissions,
		permission("portal.customer.manage", "管理门户本地客户目录", "portal_customer", "manage", "HIGH"),
		permission("portal.invite.manage", "管理门户本地邀请", "portal_invite", "manage", "HIGH"),
		permission("portal.access.disable", "停用门户客户访问", "portal_access", "disable", "CRITICAL"),
		permission("portal.local_project.manage", "管理门户本地项目授权", "portal_local_project", "manage", "HIGH"),
		permission("portal.local_report.manage", "管理门户本地报告来源", "portal_local_report", "manage", "HIGH"),
	)
	return Manifest{
		Version:     "portal-2026.11.04-v4",
		Permissions: permissions,
		Roles: []Role{
			role("portal_customer", "门户客户", "访问当前客户映射范围内的 Portal 自助能力", customerPermissions...),
			role("portal_super_admin", "门户超级管理员", "管理客户门户运营；跨客户业务数据必须经过受控客户上下文", permissionCodes(permissions)...),
		},
		Policy: Policy{MaxEffectiveRoles: 1},
	}
}

func permission(code, name, resource, action, risk string) Permission {
	return Permission{Code: code, Name: name, ResourceCode: resource, ResourceName: resource, Action: action, RiskLevel: risk}
}

// superAdminOnly 标记该权限只能由平台超级管理员授予：业务侧角色配置不能自行下发，
// 平台据此在授予链路上二次校验。只有确实需要跨数据范围的特权才使用它。
func superAdminOnly(item Permission) Permission {
	item.GrantPolicy = "SUPER_ADMIN_ONLY"
	return item
}

func role(code, name, description string, permissions ...string) Role {
	return Role{Code: code, Name: name, Description: description, Permissions: permissions}
}

func permissionCodes(permissions []Permission) []string {
	result := make([]string, 0, len(permissions))
	for _, item := range permissions {
		result = append(result, item.Code)
	}
	return result
}
