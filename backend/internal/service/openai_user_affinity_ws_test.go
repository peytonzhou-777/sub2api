package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsOpenAIWSTurnAcceptedEvent(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{eventType: "response.created", want: true},
		{eventType: "response.in_progress", want: true},
		{eventType: "response.output_text.delta", want: true},
		{eventType: "response.completed", want: true},
		{eventType: "response.done", want: true},
		{eventType: "response.failed", want: false},
		{eventType: "response.incomplete", want: false},
		{eventType: "response.cancelled", want: false},
		{eventType: "error", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIWSTurnAcceptedEvent(tt.eventType))
		})
	}
}
