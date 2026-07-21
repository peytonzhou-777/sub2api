package service

import "net/http"

// OpenAIMaskedClientError 描述隐藏上游详情后返回给客户端的稳定错误。
type OpenAIMaskedClientError struct {
	Status  int
	ErrType string
	Message string
}

// MapOpenAIMaskedUpstreamError 按本站语义映射第三方 OpenAI 上游错误。
func MapOpenAIMaskedUpstreamError(upstreamStatus int) OpenAIMaskedClientError {
	switch upstreamStatus {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return OpenAIMaskedClientError{
			Status:  http.StatusBadRequest,
			ErrType: "invalid_request_error",
			Message: "Request rejected by upstream service",
		}
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
		return OpenAIMaskedClientError{
			Status:  http.StatusBadGateway,
			ErrType: "upstream_error",
			Message: "Upstream service authentication or quota error",
		}
	case http.StatusTooManyRequests:
		return OpenAIMaskedClientError{
			Status:  http.StatusTooManyRequests,
			ErrType: "rate_limit_error",
			Message: "Service is busy, please retry later",
		}
	case 529:
		return OpenAIMaskedClientError{
			Status:  http.StatusServiceUnavailable,
			ErrType: "upstream_error",
			Message: "Service is temporarily overloaded, please retry later",
		}
	default:
		if upstreamStatus >= http.StatusInternalServerError {
			return OpenAIMaskedClientError{
				Status:  http.StatusBadGateway,
				ErrType: "upstream_error",
				Message: "Upstream service temporarily unavailable",
			}
		}
		return OpenAIMaskedClientError{
			Status:  http.StatusBadGateway,
			ErrType: "upstream_error",
			Message: "Upstream request failed",
		}
	}
}

// mapOpenAIMaskedProtocolError 根据协议错误载荷推断语义状态后生成本站错误。
func mapOpenAIMaskedProtocolError(payload []byte) OpenAIMaskedClientError {
	message := extractOpenAISSEErrorMessage(payload)
	return MapOpenAIMaskedUpstreamError(openAIStreamFailedEventSemanticStatus(payload, message))
}

// maskOpenAIImagesErrorForClient 将图片协议错误转换为不含上游详情的客户端错误。
func maskOpenAIImagesErrorForClient(upstreamErr *OpenAIImagesUpstreamError) *OpenAIImagesUpstreamError {
	status := http.StatusBadGateway
	requestID := ""
	if upstreamErr != nil {
		status = upstreamErr.clientStatusCode()
		requestID = upstreamErr.UpstreamRequestID
	}
	masked := MapOpenAIMaskedUpstreamError(status)
	return &OpenAIImagesUpstreamError{
		StatusCode:        masked.Status,
		ErrorType:         masked.ErrType,
		Message:           masked.Message,
		UpstreamRequestID: requestID,
	}
}
