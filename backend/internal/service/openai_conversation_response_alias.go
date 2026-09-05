package service

import "context"

// lookupOpenAIConversationResponseAlias 回读同一认证主体和分组内的 Responses lane，避免传输切换丢失来源。
func lookupOpenAIConversationResponseAlias(ctx context.Context, store OpenAIUserAffinityConversationStore, userID, apiKeyID int64, groupID *int64, responseID string) (*OpenAIUserConversationBinding, error) {
	var found *OpenAIUserConversationBinding
	scopes := []string{openAIUserAffinityScopeKey(groupID, true, "", "", OpenAIUpstreamTransportHTTPSSE)}
	for _, transport := range []OpenAIUpstreamTransport{OpenAIUpstreamTransportHTTPSSE, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIUpstreamTransportResponsesWebsocketV2, OpenAIUpstreamTransportResponsesWebsocket} {
		for _, capability := range []OpenAIEndpointCapability{"", OpenAIEndpointCapabilityResponses} {
			scopes = append(scopes, openAIUserAffinityScopeKey(groupID, false, capability, "", transport))
		}
	}
	for _, scope := range scopes {
		hash := openAIUserAffinityScopedStateHash(userID, apiKeyID, scope, "response_id", responseID)
		candidate, err := store.GetOpenAIUserConversationBindingByAlias(ctx, userID, apiKeyID, scope, "response_id", hash)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if found != nil && found.ID != candidate.ID {
			return nil, ErrOpenAICodexThreadLineageConflict
		}
		found = candidate
	}
	return found, nil
}
