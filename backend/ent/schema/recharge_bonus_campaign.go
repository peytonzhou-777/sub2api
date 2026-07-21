package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RechargeBonusCampaign 表示一期充值赠送限时额度活动。
type RechargeBonusCampaign struct {
	ent.Schema
}

// Annotations 指定充值活动表名。
func (RechargeBonusCampaign) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recharge_bonus_campaigns"}}
}

// Fields 定义充值活动字段。
func (RechargeBonusCampaign) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100).NotEmpty(),
		field.String("description").MaxLen(1000).Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("start_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("end_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int("participation_limit").Default(0),
		field.JSON("tiers", []domain.RechargeBonusTier{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("created_at").Immutable().Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes 定义充值活动查询索引。
func (RechargeBonusCampaign) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("start_at", "end_at"),
	}
}
