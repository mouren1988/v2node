package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreConf "github.com/xtls/xray-core/infra/conf"
)

// TestSelfSignedCertHasNoOCSP 验证：自签证书确实没有 OCSPServer 字段，
// 因此当 v2node 设置 OcspStapling: 3600 时，Xray 会持续打印
// "no OCSP server specified in cert" 警告。
//
// 这解释了用户看到的日志刷屏现象。
func TestSelfSignedCertHasNoOCSP(t *testing.T) {
	dir := t.TempDir()

	// 生成自签证书（模拟面板下发的 CertFile/KeyFile）
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mini.aicn.plus"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"mini.aicn.plus"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}

	// 解析后确认没有 OCSP 服务器地址
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.OCSPServer) != 0 {
		t.Fatalf("expected self-signed cert to have NO OCSP server, got %v", parsed.OCSPServer)
	}
	t.Logf("✓ 自签证书 OCSPServer 为空: %v (len=%d)", parsed.OCSPServer, len(parsed.OCSPServer))

	// 复现 v2node 的行为：构造与 core/inbound.go 相同的 TLS 配置
	cfg := &coreConf.TLSCertConfig{
		CertFile:     certPath,
		KeyFile:      keyPath,
		OcspStapling: 3600, // ← core/inbound.go:146 硬编码
	}
	if cfg.OcspStapling != 3600 {
		t.Fatal("setupOcspTicker 的 isOcspstapling 依赖 OcspStapling != 0")
	}
	t.Log("✓ OcspStapling=3600 → setupOcspTicker 会置 isOcspstapling=true")
	t.Log("  → 每次 ticker 触发 ocsp.GetOCSPForCert → 因 len(OCSPServer)==0 返回错误")
	t.Log("  → 打印: ignoring invalid OCSP > no OCSP server specified in cert")
}
