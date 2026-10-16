package inspect

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// row is a result row: column name → value as text, NULL as "". Numbers in
// decimal, bit as 0/1, datetime as 2006-01-02T15:04:05, binary as 0x… hex.
type row map[string]string

// querier runs the RESTORE … ONLY statements on the throwaway SQL Server.
type querier interface {
	query(ctx context.Context, stmt string) ([]row, error)
	Close() error
}

type sqlQuerier struct{ db *sql.DB }

// connectSQL logs in as sa on the throwaway SQL Server. No TLS: it listens
// on 127.0.0.1 only, its certificate is self-signed and the password is
// random and dies with the container.
func connectSQL(ctx context.Context, addr, password string) (querier, error) {
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword("sa", password), Host: addr}
	q := url.Values{}
	q.Set("database", "master")
	q.Set("encrypt", "disable")
	q.Set("dial timeout", "5")
	q.Set("app name", "dialga-inspect")
	u.RawQuery = q.Encode()
	connector, err := mssql.NewConnector(u.String())
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return sqlQuerier{db}, nil
}

func (s sqlQuerier) Close() error { return s.db.Close() }

func (s sqlQuerier) query(ctx context.Context, stmt string) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var out []row
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(row, len(cols))
		for i, c := range cols {
			r[c.Name()] = cell(vals[i], c.DatabaseTypeName())
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// cell renders a scanned value as text (see row).
func cell(v any, dbType string) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case time.Time:
		return v.Format("2006-01-02T15:04:05")
	case []byte:
		switch dbType {
		case "DECIMAL", "MONEY", "SMALLMONEY":
			return string(v)
		case "UNIQUEIDENTIFIER":
			var id mssql.UniqueIdentifier
			if id.Scan(v) == nil {
				return id.String()
			}
		}
		return "0x" + strings.ToUpper(hex.EncodeToString(v))
	}
	return ""
}

// errTerminated is the closing line of a failed RESTORE ("RESTORE HEADERONLY
// is terminating abnormally"), which says nothing the line before it does not.
const errTerminated = 3013

// sqlError is the text of a SQL Server error, the container paths replaced by
// the names of the files.
func sqlError(err error, names map[string]string) string {
	msg := err.Error()
	var e mssql.Error
	if errors.As(err, &e) {
		var msgs []string
		for _, m := range e.All {
			if m.Number != errTerminated {
				msgs = append(msgs, m.Message)
			}
		}
		msg = e.Message
		if len(msgs) > 0 {
			msg = strings.Join(msgs, " ")
		}
	}
	for disk, name := range names {
		msg = strings.ReplaceAll(msg, disk, name)
	}
	return msg
}

// sqlNumbers are the SQL Server error numbers in err.
func sqlNumbers(err error) []int32 {
	var e mssql.Error
	if !errors.As(err, &e) {
		return nil
	}
	if len(e.All) == 0 {
		return []int32{e.Number}
	}
	nums := make([]int32, 0, len(e.All))
	for _, m := range e.All {
		nums = append(nums, m.Number)
	}
	return nums
}
