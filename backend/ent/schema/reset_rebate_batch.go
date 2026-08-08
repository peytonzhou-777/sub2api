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

// ResetRebateBatch 保存重置返利批次及结算汇总。
type ResetRebateBatch struct{ ent.Schema }

func (ResetRebateBatch) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_batches"}}
}

// Fields 同时保留 v1 只读字段并定义 v2 账号维度结算字段。
func (ResetRebateBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Int("mechanism_version").Default(2),
		field.Int64("group_id").Optional().Nillable(),
		field.String("group_name").MaxLen(100).Default(""),
		field.Int64("admin_id"),
		field.String("admin_email").MaxLen(255).Default(""),
		field.Time("period_start").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("period_end").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").MaxLen(20).Default("running"),
		field.String("failure_stage").MaxLen(20).Default(""),
		field.String("execution_mode").MaxLen(16).Default(""),
		field.Int64("execution_cursor_user_id").Default(0),
		field.Int64("execution_admin_id").Optional().Nillable(),
		field.String("execution_admin_email").MaxLen(255).Default(""),
		field.Time("initial_issued_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Bool("force_stat_ratio_enabled").Default(false),
		field.String("force_stat_ratio").SchemaType(map[string]string{dialect.Postgres: "decimal(11,8)"}).Default("100"),
		field.Int("account_count").Default(0),
		field.Int("risk_account_count").Default(0),
		field.Int("progress_total").Default(0),
		field.Int("progress_completed").Default(0),
		field.String("raw_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("weighted_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("expected_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.String("successful_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.String("failed_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.String("excluded_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.Int("payout_ratio").Optional().Nillable(),
		field.String("rebate_reason").MaxLen(100).Default(""),
		field.Int("preview_version").Default(0),
		field.Int("expected_user_count").Default(0),
		field.Int("successful_user_count").Default(0),
		field.Int("excluded_user_count").Default(0),
		field.Int("failed_user_count").Default(0),
		field.String("failure_code").MaxLen(64).Default(""),
		field.String("failure_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Int64("executed_by_admin_id").Optional().Nillable(),
		field.String("executed_by_admin_email").MaxLen(255).Default(""),
		field.Time("first_executed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_retry_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateBatch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("admin_id", "created_at"),
		index.Fields("mechanism_version", "created_at"),
	}
}
