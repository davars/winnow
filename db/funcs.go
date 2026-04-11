package db

import (
	"database/sql/driver"
	"fmt"

	"github.com/dustin/go-humanize"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("human_bytes", 1, humanFunc(humanize.Bytes))
	sqlite.MustRegisterDeterministicScalarFunction("human_ibytes", 1, humanFunc(humanize.IBytes))
}

func humanFunc(format func(uint64) string) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case nil:
			return nil, nil
		case int64:
			if v < 0 {
				return nil, fmt.Errorf("negative byte count: %d", v)
			}
			return format(uint64(v)), nil
		case float64:
			if v < 0 {
				return nil, fmt.Errorf("negative byte count: %v", v)
			}
			return format(uint64(v)), nil
		default:
			return nil, fmt.Errorf("unsupported argument type %T", v)
		}
	}
}
