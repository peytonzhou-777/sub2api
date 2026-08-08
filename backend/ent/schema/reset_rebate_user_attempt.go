package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ResetRebateUserAttempt 保存每次逐用户发放尝试。
type ResetRebateUserAttempt struct{ ent.Schema }

func (ResetRebateUserAttempt) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_user_attempts"}}
}

// Fields 定义发放尝试的管理员、结果和错误摘要。
func (ResetRebateUserAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"),
		field.Int64("user_id"),
		field.Int("attempt_no"),
		field.Int64("admin_id"),
		field.String("admin_email").MaxLen(255).Default(""),
		field.String("attempt_type").MaxLen(16),
		field.String("result").MaxLen(32),
		field.String("expected_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.Int64("grant_id").Optional().Nillable(),
		field.String("error_code").MaxLen(64).Default(""),
		field.String("error_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateUserAttempt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "user_id", "attempt_no").Unique(),
		index.Fields("batch_id", "created_at"),
	}
}
