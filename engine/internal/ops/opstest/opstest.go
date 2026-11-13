// Package opstest has what the tests of other packages need to fake the
// SQL side of internal/ops.
package opstest

import (
	"context"
	"errors"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// NoAccess implements the access reads of ops.SQL for a fake that has no
// logins or users: embed it. Every read answers "nothing there". It embeds
// NoCollation, so it implements the collation reads too.
type NoAccess struct{ NoCollation }

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

// NoCollation implements the collation reads of ops.SQL (ops.CollationSQL)
// for a fake that has nothing to say about collations: embed it. Every read
// fails, and Exists finds nothing.
type NoCollation struct{}

var errNoCollation = errors.New("opstest: this fake has no collations")

func (NoCollation) ServerCollation(context.Context) (mssql.ServerCollation, error) {
	return mssql.ServerCollation{}, errNoCollation
}

func (NoCollation) Collation(context.Context, string) (mssql.CollationInfo, bool, error) {
	return mssql.CollationInfo{}, false, errNoCollation
}

func (NoCollation) CollationState(context.Context, string) (mssql.CollationState, error) {
	return mssql.CollationState{}, errNoCollation
}

func (NoCollation) DatabaseCollations(context.Context) ([]mssql.DatabaseCollation, error) {
	return nil, errNoCollation
}

func (NoCollation) Collations(context.Context) ([]mssql.CollationInfo, error) {
	return nil, errNoCollation
}

func (NoCollation) CharColumns(context.Context, string) ([]mssql.CharColumn, error) {
	return nil, errNoCollation
}

func (NoCollation) CollationBlockers(context.Context, string) ([]mssql.CollationBlocker, error) {
	return nil, errNoCollation
}

func (NoCollation) NameClashes(context.Context, string, string) ([]string, error) {
	return nil, errNoCollation
}

func (NoCollation) ConversionCatalog(context.Context, string) (mssql.ConversionCatalog, error) {
	return mssql.ConversionCatalog{}, errNoCollation
}

func (NoCollation) Exists(context.Context, string) (bool, error) { return false, nil }
