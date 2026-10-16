// Package mssql talks to the SQL Server of an RDS instance: the connection
// with the certificate verified, and the native backup/restore tasks
// (msdb.dbo.rds_*).
package mssql

import (
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/decisyon/dialga/engine/internal/rdsca"
)

// Addr is where the engine opens the TDS connection: the RDS endpoint itself
// with direct, otherwise the local tunnel port.
func Addr(direct bool, localPort int, endpoint string, endpointPort int) string {
	if direct {
		return net.JoinHostPort(endpoint, strconv.Itoa(endpointPort))
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
}

// Open prepares a connection to addr that verifies the server certificate
// against the RDS roots for the endpoint name, so it holds through a tunnel
// too. Nothing is dialed until the first query.
func Open(addr, endpoint, user, password string) (*sql.DB, error) {
	pool, err := rdsca.Pool()
	if err != nil {
		return nil, err
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(user, password),
		Host:   addr,
	}
	q := url.Values{}
	q.Set("database", "master")
	q.Set("encrypt", "true")
	q.Set("hostnameincertificate", endpoint)
	q.Set("dial timeout", "10")
	q.Set("app name", "dialga")
	u.RawQuery = q.Encode()
	cfg, err := msdsn.Parse(u.String())
	if err != nil {
		return nil, err
	}
	if cfg.TLSConfig == nil {
		return nil, errors.New("TLS configuration missing")
	}
	cfg.TLSConfig.RootCAs = pool
	cfg.TLSConfig.ServerName = endpoint
	cfg.TLSConfig.MinVersion = tls.VersionTLS12
	db := sql.OpenDB(mssql.NewConnectorConfig(cfg))
	db.SetMaxOpenConns(1)
	return db, nil
}
