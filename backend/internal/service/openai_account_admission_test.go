package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type openAIAdmissionQueueStub struct {
	mu         sync.Mutex
	enqueueErr error
	gotTicket  OpenAIAccountAdmissionTicket
	polls      []OpenAIAccountAdmissionPoll
	pollErr    error
	grantErr   error
	grantDelay time.Duration
	gotJitter  time.Duration
}

func (s *openAIAdmissionQueueStub) Enqueue(_ context.Context, ticket OpenAIAccountAdmissionTicket, _ OpenAIAccountAdmissionConfig) error {
	s.mu.Lock()
	s.gotTicket = ticket
	s.mu.Unlock()
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

func TestOpenAIAccountAdmissionCopiesPersonaSlotMetadataIntoTicket(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.Enabled = true
	cfg.QueueEnabled = true
	queue := &openAIAdmissionQueueStub{}
	svc := NewOpenAIAccountAdmissionService(nil, queue)

	_, err := svc.Acquire(context.Background(), OpenAIAccountAdmissionRequest{
		AccountID:         7,
		Persona:           " OpenCode ",
		SlotID:            1,
		SlotGeneration:    4,
		SlotSetGeneration: 9,
		CredentialChainID: "chain-opencode-1",
		MaxConcurrency:    1,
		TryAcquireSlot: func(context.Context, int64, int) (func(), bool, error) {
			return func() {}, true, nil
		},
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	queue.mu.Lock()
	got := queue.gotTicket
	queue.mu.Unlock()
	if got.Persona != "opencode" {
		t.Fatalf("ticket persona = %q, want canonical opencode", got.Persona)
	}
	if got.SlotID != 1 || got.SlotGeneration != 4 || got.SlotSetGeneration != 9 {
		t.Fatalf("ticket slot metadata = %+v", got)
	}
	if got.CredentialChainID != "chain-opencode-1" {
		t.Fatalf("ticket credential chain = %q", got.CredentialChainID)
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
	short := EstimateOpenAIAdmissionTokens([]byte(`{"model":"gpt-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`), 4096, 0, 128000)
	long := EstimateOpenAIAdmissionTokens([]byte(`{"model":"gpt-5","max_tokens":10,"messages":[{"role":"user","content":"please summarize this long message with several distinct words and details"}]}`), 4096, 0, 128000)
	if long <= short {
		t.Fatalf("messages token estimate did not grow: short=%d long=%d", short, long)
	}
}

func TestEstimateOpenAIAdmissionTokensCapsUntrustedOutputBudget(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","max_output_tokens":4194304,"input":"hello"}`)
	var req openAIInputTokensCountRequest
	if err := jsonUnmarshalOpenAIAdmission(body, &req); err != nil {
		t.Fatal(err)
	}
	input, err := estimateOpenAIInputTokens(req)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := EstimateOpenAIAdmissionTokens(body, 4096, 0, 128000), int64(input)+128000; got != want {
		t.Fatalf("model-capped estimate = %d, want %d", got, want)
	}
	if got, want := EstimateOpenAIAdmissionTokens(body, 4096, 0, 0), int64(input)+4096; got != want {
		t.Fatalf("configured-capped estimate = %d, want %d", got, want)
	}
}

func TestEstimateOpenAIAdmissionTokensCapsInputAtModelLimit(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","max_output_tokens":10,"input":"` + strings.Repeat("token ", 2000) + `"}`)
	uncapped := EstimateOpenAIAdmissionTokens(body, 4096, 0, 128000)
	capped := EstimateOpenAIAdmissionTokens(body, 4096, 100, 128000)
	if uncapped <= capped {
		t.Fatalf("input cap was not exercised: uncapped=%d capped=%d", uncapped, capped)
	}
	if capped != 110 {
		t.Fatalf("input-capped estimate = %d, want 110", capped)
	}
}

func TestEstimateOpenAIAdmissionTokensDoesNotCountToolOutputImageBase64AsText(t *testing.T) {
	base64Payload := strings.Repeat("A", 200000)
	mediaBody := []byte(`{"model":"gpt-5.6-sol","max_output_tokens":10,"input":[{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_image","image_url":"data:image/png;base64,` + base64Payload + `"}]}]}`)
	plainBody := []byte(`{"model":"gpt-5.6-sol","max_output_tokens":10,"input":[{"type":"function_call_output","call_id":"call_text","output":"` + base64Payload + `"}]}`)

	mediaEstimate := EstimateOpenAIAdmissionTokens(mediaBody, 4096, 0, 128000)
	plainEstimate := EstimateOpenAIAdmissionTokens(plainBody, 4096, 0, 128000)
	if mediaEstimate >= 1000 {
		t.Fatalf("inline image base64 was still counted as text: %d", mediaEstimate)
	}
	if plainEstimate <= mediaEstimate+10000 {
		t.Fatalf("plain tool text must remain countable: media=%d plain=%d", mediaEstimate, plainEstimate)
	}
}

func TestEstimateOpenAIAccountAdmissionTokensUsesMappedModelLimit(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.6-sol": {MaxInputTokens: 1050000, MaxOutputTokens: 128000},
	}}
	gateway := &OpenAIGatewayService{billingService: NewBillingService(nil, pricing)}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"client-alias": "gpt-5.6-sol"},
		},
	}
	body := []byte(`{"model":"client-alias","max_output_tokens":4194304,"input":"hello"}`)
	var req openAIInputTokensCountRequest
	if err := jsonUnmarshalOpenAIAdmission(body, &req); err != nil {
		t.Fatal(err)
	}
	input, err := estimateOpenAIInputTokens(req)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := gateway.EstimateOpenAIAccountAdmissionTokens(account, body, "", 4096), int64(input)+128000; got != want {
		t.Fatalf("mapped model estimate = %d, want %d", got, want)
	}

	hintBody := []byte(`{"max_output_tokens":4194304,"input":"hello"}`)
	var hintReq openAIInputTokensCountRequest
	if err := jsonUnmarshalOpenAIAdmission(hintBody, &hintReq); err != nil {
		t.Fatal(err)
	}
	hintInput, err := estimateOpenAIInputTokens(hintReq)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gateway.EstimateOpenAIAccountAdmissionTokens(account, hintBody, "gpt-5.6-sol", 4096), int64(hintInput)+128000; got != want {
		t.Fatalf("model-hint estimate = %d, want %d", got, want)
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
