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

// RechargeBonusParticipation 记录用户在一期活动中已成功获得赠送的次数。
type RechargeBonusParticipation struct {
	ent.Schema
}

// Annotations 指定活动参与记录表名。
func (RechargeBonusParticipation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recharge_bonus_participations"}}
}

// Fields 定义活动参与记录字段。
func (RechargeBonusParticipation) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("campaign_id"),
		field.Int64("user_id"),
		field.Int("completed_count").Default(0),
		field.Time("created_at").Immutable().Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes 定义活动参与记录索引。
func (RechargeBonusParticipation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("campaign_id", "user_id").Unique(),
		index.Fields("user_id"),
	}
}
