// Package opstest has what the tests of other packages need to fake the
// SQL side of internal/ops.
package opstest

import (
	"context"
	"errors"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// NoAccess implements the access reads of ops.SQL for a fake that has no
// logins or users: embed it. Every read answers "nothing there".
type NoAccess struct{}

var errNoAccess = errors.New("opstest: this fake has no logins or users")

func (NoAccess) Logins(context.Context, string) ([]mssql.Login, error) { return nil, nil }

func (NoAccess) DatabaseAccess(context.Context, string, string) (mssql.DatabaseAccess, error) {
	return mssql.DatabaseAccess{}, errNoAccess
}

func (NoAccess) MsdbUser(context.Context, string) (mssql.MsdbUser, error) {
	return mssql.MsdbUser{}, nil
}

func (NoAccess) BackupOperators(context.Context, string) ([]string, []string, error) {
	return nil, nil, nil
}

func (NoAccess) UserSchemas(context.Context, string, string) ([]string, error) { return nil, nil }
