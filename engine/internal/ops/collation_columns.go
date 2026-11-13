// Work package C: see the collation design.

package ops

import (
	"context"

	"github.com/decisyon/dialga/engine/internal/mssql"
	"github.com/decisyon/dialga/engine/internal/validate"
)

// validateConvertColumns is Validate for convert-columns, after what the
// collation operations share (validateCollation).
func validateConvertColumns(p Params) error {
	if err := validate.DBName(p.Database); err != nil {
		return err
	}
	return errNotYet
}

// ConvertColumns prepares the conversion of the columns of a database to a
// collation (collation design §6.2).
func (s *Session) ConvertColumns(ctx context.Context, in Params) (*Prepared, error) {
	return nil, errNotYet
}

// runConvert runs one unit of a convert-columns (collation design §6.2).
func (s *Session) runConvert(ctx context.Context, st CollationStep) (string, error) {
	return "", errNotYet
}

// planUnits is the plan of a conversion: the units to convert, the tables
// refused and the notes for the checks (collation design §6.2, step 4).
func planUnits(cat mssql.ConversionCatalog, target string, tables []mssql.TableRef) (units []mssql.ConversionUnit, refused []RefusedTable, notes []string) {
	return nil, nil, nil
}

// planUnits is used by ConvertColumns once it is written.
var _ = planUnits
