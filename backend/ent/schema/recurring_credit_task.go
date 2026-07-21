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

// RecurringCreditTask 保存管理员配置的循环限时额度任务。
type RecurringCreditTask struct{ ent.Schema }

func (RecurringCreditTask) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recurring_credit_tasks"}}
}

// Fields 定义循环任务的结构化计划、生命周期和并发版本字段。
func (RecurringCreditTask) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100),
		field.String("admin_notes").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.String("schedule_type").MaxLen(16),
		field.Int("day_of_month").Optional().Nillable(),
		field.Int("day_of_week").Optional().Nillable(),
		field.String("local_time").MaxLen(5),
		field.String("timezone").MaxLen(64),
		field.Float("amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Int("validity_days").Optional().Nillable(),
		field.String("execution_mode").MaxLen(16),
		field.Int("remaining_runs").Optional().Nillable(),
		field.Int("skip_count").Default(0),
		field.String("status").MaxLen(16),
		field.Time("next_run_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("version").Default(1),
		field.String("idempotency_key").Optional().Nillable().MaxLen(128),
		field.Int64("created_by_admin_id"),
		field.String("created_by_admin_email").MaxLen(255).Default(""),
		field.Int64("updated_by_admin_id"),
		field.String("updated_by_admin_email").MaxLen(255).Default(""),
		field.Time("deleted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (RecurringCreditTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "next_run_at"),
		index.Fields("created_at"),
		index.Fields("name"),
		index.Fields("idempotency_key").Unique(),
	}
}
