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

// ResetRebateBatch 保存一次重置返利统计任务及其不可变快照摘要。
type ResetRebateBatch struct{ ent.Schema }

func (ResetRebateBatch) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_batches"}}
}

// Fields 定义返利批次生命周期、统计口径和执行审计字段。
func (ResetRebateBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id"),
		field.String("group_name").MaxLen(100),
		field.Int64("admin_id"),
		field.String("admin_email").MaxLen(255).Default(""),
		field.Time("period_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("period_end").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").MaxLen(20).Default("running"),
		field.Int("progress_total").Default(0),
		field.Int("progress_completed").Default(0),
		field.Int("progress_succeeded").Default(0),
		field.Int("progress_failed").Default(0),
		field.Int("participant_count").Default(0),
		field.Float("actual_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Float("refundable_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Float("failed_account_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Float("weekly_usage_percent").SchemaType(map[string]string{dialect.Postgres: "decimal(12,8)"}).Default(0),
		field.Float("refundable_percent").SchemaType(map[string]string{dialect.Postgres: "decimal(12,8)"}).Default(0),
		field.Int("suggested_ratio").Default(0),
		field.Int("configured_ratio").Optional().Nillable(),
		field.Int("issued_user_count").Default(0),
		field.Int("excluded_user_count").Default(0),
		field.Float("issued_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.String("failure_code").MaxLen(64).Default(""),
		field.String("failure_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.String("rebate_reason").MaxLen(100).Default(""),
		field.Int("execution_attempts").Default(0),
		field.Time("completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("snapshot_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("issued_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("executed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateBatch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id", "period_start", "period_end"),
		index.Fields("admin_id", "created_at"),
		index.Fields("status", "created_at"),
	}
}
