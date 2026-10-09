package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// realityPublicKey 由服务端私钥推出公钥。已有的 REALITY 入站配置里只有私钥，
// 分享链接却要公钥。
func realityPublicKey(priv string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(priv, "="))
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// certInfo 读取证书：是否自签、SHA-256 指纹（十六进制）、CN。
//
// 自签证书验不过 CA，分享链接要带 insecure / 指纹，客户端才连得上。
func certInfo(certFile string) (selfSigned bool, sha string, cn string, err error) {
	blob, err := os.ReadFile(certFile)
	if err != nil {
		return false, "", "", fmt.Errorf("读取证书失败: %w", err)
	}
	block, _ := pem.Decode(blob)
	if block == nil {
		return false, "", "", fmt.Errorf("%s 不是 PEM 证书", certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, "", "", fmt.Errorf("解析证书失败: %w", err)
	}
	sum := sha256.Sum256(cert.Raw)
	// 不用 CheckSignatureFrom：它要求签发者是 CA，而自签的叶子证书通常不是
	selfSigned = bytes.Equal(cert.RawIssuer, cert.RawSubject) &&
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
	cn = cert.Subject.CommonName
	if cn == "" && len(cert.DNSNames) > 0 {
		cn = cert.DNSNames[0]
	}
	return selfSigned, hex.EncodeToString(sum[:]), cn, nil
}

// quicProtocols 跑在 QUIC 上（UDP），分享链接要带 alpn=h3 等参数。
var quicProtocols = map[string]bool{"tuic": true, "hysteria2": true}
