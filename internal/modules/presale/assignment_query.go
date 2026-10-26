package presale

import "context"

func (s *Service) Assignments(ctx context.Context, actor Actor, id uint64) ([]Assignment, error) {
	if !actor.Can("presale.read") {
		return nil, ErrForbidden
	}
	requestValue, e := s.repo.FindRequest(ctx, actor.TenantID, id)
	if e != nil {
		return nil, e
	}
	if e = s.requireReadable(ctx, actor, requestValue); e != nil {
		return nil, e
	}
	return s.repo.ListAssignments(ctx, actor.TenantID, id)
}

// 详情读取与列表使用相同资源范围：管理角色可读租户内任务，销售只读本人申请，
// 被分派的基础平台用户可读其当前或历史分派。分派角色不从 OIDC 角色猜测，
// 因而猜中租户内申请 ID 也不能绕过真实业务关系。
