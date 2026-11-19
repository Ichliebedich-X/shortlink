-- 短链接项目 · 表结构

CREATE DATABASE IF NOT EXISTS shortlink
  DEFAULT CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

USE shortlink;

-- 短链主表
CREATE TABLE IF NOT EXISTS t_link (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '自增主键，拿它转 base62',
    code        VARCHAR(16)     NOT NULL                COMMENT '短码，写入 Redis 时作为 key 的一部分',
    origin      VARCHAR(2048)   NOT NULL                COMMENT '原始长链接',
    gid         VARCHAR(32)     NOT NULL DEFAULT 'default' COMMENT '分组/租户标识，未来分库分表的分片键',
    status      TINYINT         NOT NULL DEFAULT 1      COMMENT '1 启用 0 禁用（禁用后跳转返回 404/403）',
    expire_at   DATETIME        NULL                    COMMENT '过期时间，NULL 表示永不过期',
    del_flag    TINYINT         NOT NULL DEFAULT 0      COMMENT '逻辑删除 0 未删 1 已删',
    created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    -- 唯一索引是"防重复"的最后一道防线：应用层判重可能被并发绕过，数据库约束不会
    UNIQUE KEY uk_code (code),
    -- 按 gid 查询短链列表时会用到（最左前缀）
    KEY idx_gid_created (gid, created_at)
    ) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '短链映射表';

-- 分组表：对应"按分组管理短链"的能力
CREATE TABLE IF NOT EXISTS t_group (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    gid         VARCHAR(32)     NOT NULL COMMENT '对外暴露的分组标识，不暴露自增 id',
    name        VARCHAR(64)     NOT NULL COMMENT '分组名',
    del_flag    TINYINT         NOT NULL DEFAULT 0,
    created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_gid (gid)
    ) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '分组表';

-- 访问统计表：按天聚合，由后台异步任务批量写入（跳转链路不直接写它）
CREATE TABLE IF NOT EXISTS t_link_access_stats (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    code        VARCHAR(16)     NOT NULL COMMENT '短码',
    stat_date   DATE            NOT NULL COMMENT '统计日期',
    pv          BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '页面访问量（每次访问+1）',
    uv          BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '独立访客数（按 IP/UV hash 去重）',
    created_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    -- 同一短码同一天只有一行，异步批量写入用 INSERT ... ON DUPLICATE KEY UPDATE
    UNIQUE KEY uk_code_date (code, stat_date)
    ) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '短链按天访问统计';
