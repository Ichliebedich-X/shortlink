package dao

import (
	"context"

	"gorm.io/gorm"
)

type StatsDao struct {
	db *gorm.DB
}

// NewStatsDao 创建一个访问访问统计表的 DAO。
func NewStatsDao(db *gorm.DB) *StatsDao {
	return &StatsDao{db: db}
}

// IncrPV 幂等写入：当天已有记录就累加，没有就新建
func (d *StatsDao) IncrPV(ctx context.Context, code string, date string, pv int64) error {
	// 这里直接用 Exec 执行原生 SQL，因为 GORM 的链式调用写这种冲突更新有点绕
	return d.db.WithContext(ctx).Exec(`
		INSERT INTO t_link_access_stats (code, stat_date, pv)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE pv = pv + VALUES(pv)
	`, code, date, pv).Error
}
