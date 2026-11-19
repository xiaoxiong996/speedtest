package main

// 面板配置持久化
//
// 为什么需要：面板里改的白名单、DDNS 规则、Cloudflare Token，如果只存在内存里，
// 一重启就全丢、回到 .env 的值 —— 用户会以为"保存了却没用"。
//
// 这里把它们写到 <certs>/panel.json（挂载卷里，容器重建也不丢），
// 启动时优先读它，覆盖 .env 的对应项。.env 退化成"首次安装的默认值"。
//
// 权限 0600：里面含 Cloudflare Token，不能让同机其他用户读到。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// panelConfig 面板可持久化的全部配置。
type panelConfig struct {
	// Cloudflare API Token（用于签发证书 + DDNS）
	CFToken string `json:"cfToken,omitempty"`

	// 访问限制
	AllowHosts   []string `json:"allowHosts"`
	AllowCIDRs   []string `json:"allowCIDRs"`
	AllowOrigins []string `json:"allowOrigins"`

	// DDNS 规则
	DDNS []ddnsEntry `json:"ddns"`

	// 证书续期阈值（天）
	RenewDays int `json:"renewDays,omitempty"`

	// 面板自己的账号 / 入口 / 会话密钥（登录后改的，重启仍然生效）
	PanelPath     string `json:"panelPath,omitempty"`
	PanelUser     string `json:"panelUser,omitempty"`
	PanelSalt     string `json:"panelSalt,omitempty"`
	PanelHash     string `json:"panelHash,omitempty"`
	PanelIter     int    `json:"panelIter,omitempty"`
	PanelSecret   string `json:"panelSecret,omitempty"`
	PanelMustChg bool   `json:"panelMustChange,omitempty"`

	// SetupDone 首次安装安全锁：装好并登录过一次后置 true，之后不再要求安装码。
	SetupDone bool `json:"setupDone,omitempty"`

	SavedAt string `json:"savedAt,omitempty"`
}

// panelConfigPath 配置文件路径（跟证书放一起，确保在可写卷里）。
func panelConfigPath(certDir string) string {
	return filepath.Join(certDir, "panel.json")
}

// loadPanelConfig 读配置；文件不存在不算错误。
func loadPanelConfig(certDir string) *panelConfig {
	raw, err := os.ReadFile(panelConfigPath(certDir))
	if err != nil {
		return nil
	}
	var cfg panelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Printf("面板配置：%s 解析失败（改用 .env 的值）: %v", panelConfigPath(certDir), err)
		return nil
	}
	return &cfg
}

// savePanelConfig 原子写盘。
func savePanelConfig(certDir string, cfg *panelConfig) error {
	cfg.SavedAt = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(panelConfigPath(certDir), b, 0o600)
}
