module speedtest

go 1.26.0

// 固定工具链小版本：1.26.11 含 12 项安全修复（CVE-2026-97033~97043、97054），
// 避免在别的机器上 go build 时悄悄退回旧版本
toolchain go1.26.11

require golang.org/x/crypto v0.59.0
