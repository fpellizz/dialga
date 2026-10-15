// Package rdsca embeds the Amazon RDS certificate bundle, so SQL Server
// connections are verified even through the SSM tunnel (127.0.0.1): the
// server name checked is the RDS endpoint, the roots are these.
//
// Source: https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem
// (all regions, 114 certificates, sha256 90a6f1a7…f21b, downloaded
// 2026-10-15). Refresh it when AWS rotates the RDS CAs.
package rdsca

import (
	"crypto/x509"
	_ "embed"
	"errors"
)

//go:embed global-bundle.pem
var bundle []byte

// Pool returns a certificate pool holding every RDS root and intermediate.
func Pool() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle) {
		return nil, errors.New("rdsca: no certificate found in the embedded bundle")
	}
	return pool, nil
}
