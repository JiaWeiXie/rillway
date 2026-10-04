package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"rillway/internal/config"
	"strings"
	"time"
)

func EnsureCredentials(c config.Config) (string, string, error) {
	token, err := os.ReadFile(c.Security.AdminTokenFile)
	if os.IsNotExist(err) {
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return "", "", err
		}
		token = []byte(hex.EncodeToString(b))
		err = config.WritePrivate(c.Security.AdminTokenFile, append(token, '\n'))
	}
	if err != nil {
		return "", "", err
	}
	if len(strings.TrimSpace(string(token))) < 32 {
		return "", "", fmt.Errorf("admin token must contain at least 32 characters")
	}
	_, certErr := os.Stat(c.Security.TLSCertFile)
	_, keyErr := os.Stat(c.Security.TLSKeyFile)
	if os.IsNotExist(certErr) && os.IsNotExist(keyErr) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return "", "", err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return "", "", err
		}
		cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Rillway local management"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
		host, _, _ := net.SplitHostPort(c.Listeners.Admin)
		if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else if host != "" && ip == nil {
			cert.DNSNames = append(cert.DNSNames, host)
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
		if err != nil {
			return "", "", err
		}
		priv, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return "", "", err
		}
		if err = config.WritePrivate(c.Security.TLSKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})); err != nil {
			return "", "", err
		}
		if err = config.WritePrivate(c.Security.TLSCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
			return "", "", err
		}
	}
	pair, err := tls.LoadX509KeyPair(c.Security.TLSCertFile, c.Security.TLSKeyFile)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(pair.Certificate[0])
	return strings.TrimSpace(string(token)), hex.EncodeToString(digest[:]), nil
}
