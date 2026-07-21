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

// RecurringCreditBatch 保存单个计划时点的执行快照与结果。
type RecurringCreditBatch struct{ ent.Schema }

func (RecurringCreditBatch) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recurring_credit_batches"}}
}

// Fields 定义批次资格窗口、租约、统计结果和失败审计字段。
func (RecurringCreditBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("task_id"),
		field.String("task_name").MaxLen(100),
		field.Time("scheduled_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("qualification_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("qualification_end").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("qualification_cutoff_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("config_version"),
		field.String("eligibility_policy").MaxLen(40).Default("period_usage_or_recharge"),
		field.String("schedule_type").MaxLen(16),
		field.Int("day_of_month").Optional().Nillable(),
		field.Int("day_of_week").Optional().Nillable(),
		field.String("local_time").MaxLen(5),
		field.String("timezone").MaxLen(64),
		field.Float("amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Int("validity_days").Optional().Nillable(),
		field.String("execution_mode").MaxLen(16),
		field.String("status").MaxLen(16),
		field.Time("claimed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("lease_owner").MaxLen(128).Default(""),
		field.Time("lease_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("heartbeat_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("attempt_count").Default(0),
		field.Int("eligible_user_count").Default(0),
		field.Int("issued_user_count").Default(0),
		field.Int("excluded_user_count").Default(0),
		field.Int("usage_eligible_count").Default(0),
		field.Int("recharge_eligible_count").Default(0),
		field.Int("api_active_count").Default(0),
		field.Int("site_active_count").Default(0),
		field.Int("both_active_count").Default(0),
		field.Time("snapshot_completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Float("issued_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.String("failure_code").MaxLen(64).Default(""),
		field.String("failure_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Time("finished_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (RecurringCreditBatch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_id", "scheduled_at").Unique(),
		index.Fields("task_id", "created_at"),
		index.Fields("status", "lease_expires_at"),
	}
}
