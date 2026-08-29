package service

import "context"

// openAIGroupPrivacyRequirementContextKey 缓存一次选号流程的分组隐私要求。
type openAIGroupPrivacyRequirementContextKey struct{}

type openAIGroupPrivacyRequirement struct {
	groupID  int64
	required bool
}

func (s *OpenAIGatewayService) withOpenAIGroupPrivacyRequirement(ctx context.Context, groupID *int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupPrivacyRequirementContextKey{}, openAIGroupPrivacyRequirement{
		groupID:  derefGroupID(groupID),
		required: s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID),
	})
}

func (s *OpenAIGatewayService) openAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if ctx != nil {
		if cached, ok := ctx.Value(openAIGroupPrivacyRequirementContextKey{}).(openAIGroupPrivacyRequirement); ok && cached.groupID == derefGroupID(groupID) {
			return cached.required
		}
	}
	return s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID)
}

func (s *OpenAIGatewayService) loadOpenAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if s == nil || groupID == nil || s.schedulerSnapshot == nil {
		return false
	}
	group, err := s.schedulerSnapshot.GetGroupByID(ctx, *groupID)
	if err != nil {
		// 查询失败时保持安全侧：不放行缺少隐私收敛状态的账号。
		return true
	}
	return group != nil && group.RequirePrivacySet
}
