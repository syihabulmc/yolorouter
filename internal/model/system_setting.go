package model

import "time"

// SystemSetting is one row of the key-value global settings table. CSP uses
// keys 'custom_system_prompt_enabled' (value 'true'/'false') and
// 'custom_system_prompt' (the text). Version drives optimistic-lock CAS on PUT.
type SystemSetting struct {
	Key       string    `gorm:"column:key;primaryKey" json:"key"`
	Value     string    `gorm:"column:value" json:"value"`
	Version   int64     `gorm:"column:version" json:"version"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (SystemSetting) TableName() string { return "system_settings" }
