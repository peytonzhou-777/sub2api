package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SecurityDepositRiskProfile 保存用户保证金风险次数和当前线性倍率。
type SecurityDepositRiskProfile struct {
	ent.Schema
}

func (SecurityDepositRiskProfile) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_risk_profiles"}}
}

func (SecurityDepositRiskProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (SecurityDepositRiskProfile) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id").Unique(),
		field.Int64("cyber_strike_count").Default(0).Min(0),
		field.Int64("risk_multiplier").Default(1).Min(1),
		field.Int64("last_violation_id").Optional().Nillable(),
		field.Int64("version").Default(1).Min(1),
	}
}

func (SecurityDepositRiskProfile) Indexes() []ent.Index {
	return []ent.Index{index.Fields("last_violation_id")}
}
