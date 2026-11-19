// Package model 定义数据库表对应的 Go 结构体。
package model

import "time"

type TLink struct {
	ID        uint64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Code      string     `gorm:"column:code;size:16;uniqueIndex" json:"code"`
	Origin    string     `gorm:"column:origin;size:2048" json:"origin"`
	GID       string     `gorm:"column:gid;size:16" json:"gid"`
	Status    int8       `gorm:"column:status;default:1" json:"status"`
	ExpireAt  *time.Time `gorm:"column:expire_at" json:"expire_at"`
	DelFlag   int8       `gorm:"column:del_flag;default:0" json:"del_flag"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (TLink) TableName() string {
	return "t_link"
}
