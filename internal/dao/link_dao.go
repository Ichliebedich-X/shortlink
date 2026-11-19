// Package dao 封装 MySQL 访问，不包含业务规则。
package dao

import (
	"context"
	"errors"
	"shortlink/internal/model"

	"gorm.io/gorm"
)

// ErrNotFound 表示"数据库里没有这条记录"。
// dao 把 GORM 的错误翻译成自己的错误，service 就不需要知道底层用的是哪个 ORM。
var ErrNotFound = errors.New("short link not found")

type LinkDao struct {
	db *gorm.DB
}

// NewLinkDao 创建一个使用指定数据库连接的短链 DAO。
func NewLinkDao(db *gorm.DB) *LinkDao {
	return &LinkDao{db: db}
}

// Create 写入一条短链记录，唯一索引冲突时返回可识别的错误。
func (d *LinkDao) Create(ctx context.Context, link *model.TLink) error {
	//把 HTTP 请求的上下文传进去。如果用户中途关掉浏览器，SQL 查询会被取消，节省数据库资源。
	return d.db.WithContext(ctx).Create(link).Error
}

// IsDuplicateKey 判断一次写库失败是不是"唯一键冲突"。
// 短链场景下这意味着：算出来的 code 已经被别的记录占用了。
// 依赖 main.go 里 gorm.Config{TranslateError: true}，
// 否则 GORM 返回的是 MySQL 原生错误，这里判断不到。
func IsDuplicateKey(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey)
}

// GetByCode 按短码查询；查不到时返回 ErrNotFound。
func (d *LinkDao) GetByCode(ctx context.Context, code string) (*model.TLink, error) {
	var link model.TLink

	// 1. 使用 GORM 查询
	err := d.db.WithContext(ctx).Where("code = ?", code).First(&link).Error

	// 2. 核心：翻译错误
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &link, nil
}

// ListAllCodes 获取所有未删除的短码。
// 为什么需要这个方法？
// 因为布隆过滤器是存在 Redis 内存里的，如果 Redis 重启或者清理了数据，
// 过滤器就空了。服务启动时必须查一次数据库，把所有历史短码重新灌入过滤器，
// 否则所有老短链都会被误判为“不存在”直接 404。
func (d *LinkDao) ListAllCodes(ctx context.Context) ([]string, error) {
	var codes []string
	// Pluck 专门用于只查询单列的值，性能比 Select("*") 好。
	// del_flag = 0 保证只加载有效短码，已被软删除的不加载。
	err := d.db.WithContext(ctx).
		Model(&model.TLink{}).
		Where("del_flag = 0").
		Pluck("code", &codes).Error
	return codes, err
}
