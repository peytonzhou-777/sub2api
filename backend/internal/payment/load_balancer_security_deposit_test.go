package payment

import (
	"context"
	"testing"
)

func TestWithRefundEnabledOnly(t *testing.T) {
	if refundEnabledOnlyFromContext(context.Background()) {
		t.Fatal("普通支付不应要求支付实例启用退款")
	}
	if !refundEnabledOnlyFromContext(WithRefundEnabledOnly(context.Background())) {
		t.Fatal("保证金支付应只选择启用退款的支付实例")
	}
}
