package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type openAIAdmissionQueueStub struct {
	mu         sync.Mutex
	enqueueErr error
	polls      []OpenAIAccountAdmissionPoll
	pollErr    error
	grantErr   error
	grantDelay time.Duration
	gotJitter  time.Duration
}

func (s *openAIAdmissionQueueStub) Enqueue(context.Context, OpenAIAccountAdmissionTicket, OpenAIAccountAdmissionConfig) error {
	return s.enqueueErr
}
func (s *openAIAdmissionQueueStub) Poll(context.Context, OpenAIAccountAdmissionTicket, OpenAIAccountAdmissionConfig) (OpenAIAccountAdmissionPoll, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pollErr != nil {
		return OpenAIAccountAdmissionPoll{}, s.pollErr
	}
	if len(s.polls) == 0 {
		return OpenAIAccountAdmissionPoll{Selected: true}, nil
	}
	result := s.polls[0]
	s.polls = s.polls[1:]
	return result, nil
}
func (s *openAIAdmissionQueueStub) Grant(_ context.Context, _ OpenAIAccountAdmissionTicket, _ OpenAIAccountAdmissionConfig, jitter time.Duration) (OpenAIAccountAdmissionGrant, error) {
	s.gotJitter = jitter
	return OpenAIAccountAdmissionGrant{Granted: true, Delay: s.grantDelay}, s.grantErr
}

func TestOpenAIAccountAdmissionMapsExpiredTicketToTimeout(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.Enabled = true
	cfg.QueueEnabled = true
	svc := NewOpenAIAccountAdmissionService(nil, &openAIAdmissionQueueStub{pollErr: ErrOpenAIAdmissionTicketGone})
	_, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
		AccountID: 1, MaxConcurrency: 1,
		TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) { return func() {}, true, nil },
	}, cfg)
	var admissionErr *OpenAIAccountAdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Kind != OpenAIAdmissionErrorTimeout || admissionErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expired ticket error = %#v", err)
	}
}

func TestOpenAIAccountAdmissionMapsCapacityRejectionsToServiceUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		queue     *openAIAdmissionQueueStub
		configure func(*OpenAIAccountAdmissionConfig)
		wantKind  OpenAIAccountAdmissionErrorKind
	}{
		{
			name:     "队列已满",
			queue:    &openAIAdmissionQueueStub{enqueueErr: ErrOpenAIAdmissionQueueFull},
			wantKind: OpenAIAdmissionErrorQueueFull,
		},
		{
			name:  "未启用排队",
			queue: &openAIAdmissionQueueStub{polls: []OpenAIAccountAdmissionPoll{{Selected: false}}},
			configure: func(cfg *OpenAIAccountAdmissionConfig) {
				cfg.QueueEnabled = false
			},
			wantKind: OpenAIAdmissionErrorQueueDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultOpenAIAccountAdmissionConfig()
			cfg.Enabled = true
			cfg.QueueEnabled = true
			if tt.configure != nil {
				tt.configure(&cfg)
			}
			svc := NewOpenAIAccountAdmissionService(nil, tt.queue)
			_, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
				AccountID: 1, MaxConcurrency: 1,
				TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) { return func() {}, true, nil },
			}, cfg)
			var admissionErr *OpenAIAccountAdmissionError
			if !errors.As(err, &admissionErr) || admissionErr.Kind != tt.wantKind || admissionErr.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("capacity rejection error = %#v", err)
			}
		})
	}
}

func TestOpenAIAccountAdmissionDoesNotShortenFinalJitter(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.Enabled = true
	cfg.QueueEnabled = true
	cfg.MaxWaitSeconds = 1
	cfg.JitterMinMS = 2000
	cfg.JitterMaxMS = 2000
	released := false
	queue := &openAIAdmissionQueueStub{
		polls:      []OpenAIAccountAdmissionPoll{{Selected: false}, {Selected: true}},
		grantDelay: 2 * time.Second,
	}
	svc := NewOpenAIAccountAdmissionService(nil, queue)
	_, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
		AccountID: 1, MaxConcurrency: 1,
		TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) {
			return func() { released = true }, true, nil
		},
	}, cfg)
	var admissionErr *OpenAIAccountAdmissionError
	if !errors.As(err, &admissionErr) || admissionErr.Kind != OpenAIAdmissionErrorTimeout {
		t.Fatalf("insufficient jitter budget error = %#v", err)
	}
	if !released {
		t.Fatal("account slot was not released after jitter budget rejection")
	}
}

func TestEstimateOpenAIAdmissionTokensSupportsMessages(t *testing.T) {
	short := EstimateOpenAIAdmissionTokens([]byte(`{"model":"gpt-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`), 4096)
	long := EstimateOpenAIAdmissionTokens([]byte(`{"model":"gpt-5","max_tokens":10,"messages":[{"role":"user","content":"please summarize this long message with several distinct words and details"}]}`), 4096)
	if long <= short {
		t.Fatalf("messages token estimate did not grow: short=%d long=%d", short, long)
	}
}
func (s *openAIAdmissionQueueStub) Remove(context.Context, OpenAIAccountAdmissionTicket) error {
	return nil
}

func TestOpenAIAccountAdmissionJittersOnlyAfterQueueing(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.Enabled = true
	cfg.QueueEnabled = true
	cfg.JitterMinMS = 17
	cfg.JitterMaxMS = 17

	t.Run("immediate", func(t *testing.T) {
		queue := &openAIAdmissionQueueStub{}
		svc := NewOpenAIAccountAdmissionService(nil, queue)
		result, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
			AccountID: 1, MaxConcurrency: 1,
			TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) { return func() {}, true, nil },
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if result.Queued || queue.gotJitter != 0 || result.Jitter != 0 {
			t.Fatalf("immediate request was jittered: result=%+v jitter=%s", result, queue.gotJitter)
		}
	})

	t.Run("queued", func(t *testing.T) {
		queue := &openAIAdmissionQueueStub{
			polls:      []OpenAIAccountAdmissionPoll{{Selected: false}, {Selected: true}},
			grantDelay: 17 * time.Millisecond,
		}
		svc := NewOpenAIAccountAdmissionService(nil, queue)
		result, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
			AccountID: 1, MaxConcurrency: 1,
			TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) { return func() {}, true, nil },
		}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Queued || queue.gotJitter != 17*time.Millisecond || result.Jitter != 17*time.Millisecond {
			t.Fatalf("queued request jitter mismatch: result=%+v jitter=%s", result, queue.gotJitter)
		}
		if result.QueueWait < 35*time.Millisecond {
			t.Fatalf("queue wait did not include polling and final jitter: %s", result.QueueWait)
		}
	})
}

func TestClassifyOpenAIAdmissionClassDetectsSubagent(t *testing.T) {
	headers := make(http.Header)
	headers.Set("x-openai-subagent", "reviewer")
	if got := ClassifyOpenAIAdmissionClass(headers, nil); got != OpenAIAdmissionBackground {
		t.Fatalf("class = %s, want background", got)
	}
	if got := ClassifyOpenAIAdmissionClass(nil, []byte(`{"model":"gpt-5"}`)); got != OpenAIAdmissionInteractive {
		t.Fatalf("class = %s, want interactive", got)
	}
}

func TestRequestTypeAdmissionRejected(t *testing.T) {
	if !RequestTypeAdmissionRejected.IsValid() {
		t.Fatal("admission rejected request type must be valid")
	}
	if got := RequestTypeAdmissionRejected.String(); got != "admission_rejected" {
		t.Fatalf("request type string = %q", got)
	}
	parsed, err := ParseUsageRequestType("admission_rejected")
	if err != nil || parsed != RequestTypeAdmissionRejected {
		t.Fatalf("parse admission rejected = %v, %v", parsed, err)
	}
}
