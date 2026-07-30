package syncplay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdx509 "crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"

	"github.com/tjfoc/gmsm/gmtls"
	"github.com/tjfoc/gmsm/sm2"
	smx509 "github.com/tjfoc/gmsm/x509"
)

// CertSet 包含国密双证书 + 标准证书，用于自动协商。
type CertSet struct {
	GMSignCert *gmtls.Certificate // SM2 签名证书
	GMEncCert  *gmtls.Certificate // SM2 加密证书
	StdCert    *gmtls.Certificate // 标准 ECDSA 证书（兼容标准客户端）
}

// GenerateCertSet 生成全部证书（纯内存，不写盘）。
func GenerateCertSet() (*CertSet, error) {
	gmSign, err := generateSM2Cert()
	if err != nil {
		return nil, fmt.Errorf("生成 SM2 签名证书: %w", err)
	}

	gmEnc, err := generateSM2Cert()
	if err != nil {
		return nil, fmt.Errorf("生成 SM2 加密证书: %w", err)
	}

	stdCert, err := generateECDSACert()
	if err != nil {
		return nil, fmt.Errorf("生成标准证书: %w", err)
	}

	return &CertSet{
		GMSignCert: gmSign,
		GMEncCert:  gmEnc,
		StdCert:    stdCert,
	}, nil
}

// generateSM2Cert 生成 SM2 自签名证书（SM2WithSM3）。
func generateSM2Cert() (*gmtls.Certificate, error) {
	privKey, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("生成序列号: %w", err)
	}

	template := &smx509.Certificate{
		SerialNumber:       serialNumber,
		Subject:            pkix.Name{Organization: []string{"SyncMedia"}, CommonName: "syncmedia"},
		NotBefore:          time.Now().Add(-1 * time.Hour),
		NotAfter:           time.Now().AddDate(10, 0, 0),
		KeyUsage:           smx509.KeyUsageDigitalSignature | smx509.KeyUsageKeyEncipherment,
		ExtKeyUsage:        []smx509.ExtKeyUsage{smx509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:               false,
		DNSNames:           []string{"localhost"},
		SignatureAlgorithm: smx509.SM2WithSM3,
	}

	derBytes, err := smx509.CreateCertificate(template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("创建证书: %w", err)
	}

	return &gmtls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  privKey,
	}, nil
}

// generateECDSACert 生成标准 ECDSA P-256 自签名证书（TLS 1.3 兼容）。
func generateECDSACert() (*gmtls.Certificate, error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("生成序列号: %w", err)
	}

	template := &stdx509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{Organization: []string{"SyncMedia"}, CommonName: "syncmedia"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              stdx509.KeyUsageDigitalSignature | stdx509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []stdx509.ExtKeyUsage{stdx509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              []string{"localhost", "*"},
	}

	derBytes, err := stdx509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("创建证书: %w", err)
	}

	return &gmtls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  privKey,
	}, nil
}

// NewAutoSwitchConfig 创建支持国密/标准 TLS 自动协商的配置。
// 标准客户端（Kazumi）→ TLS 1.3；国密客户端 → GM SSL。
func NewAutoSwitchConfig(certs *CertSet) (*gmtls.Config, error) {
	return gmtls.NewBasicAutoSwitchConfig(certs.GMSignCert, certs.GMEncCert, certs.StdCert)
}
