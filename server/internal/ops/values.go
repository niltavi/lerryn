package ops

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// timeLayout is how Bun writes times: UTC, microseconds, trailing zeros
// trimmed. SQLite stores times as this text and compares it as text, so
// a time read from PostgreSQL must be written back in exactly this form
// (tech design 6.3).
const timeLayout = "2006-01-02 15:04:05.999999-07:00"

// bytesKey marks a JSON object holding binary data in base64. No lerryn
// table has binary columns today; the encoding keeps a future one from
// being silently mangled into text.
const bytesKey = "$bytes"

// encodeValue turns a value scanned from either database into its
// portable JSON form.
func encodeValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, int64, float64, string:
		return x, nil
	case int32:
		return int64(x), nil
	case int:
		return int64(x), nil
	case time.Time:
		return x.UTC().Format(timeLayout), nil
	case []byte:
		return map[string]string{bytesKey: base64.StdEncoding.EncodeToString(x)}, nil
	case [16]byte:
		// A UUID handed over in binary form.
		return uuid.UUID(x).String(), nil
	default:
		return nil, fmt.Errorf("unsupported value type %T", v)
	}
}

// decodeValue turns a portable JSON value into what Bun formats correctly
// for the target column: SQLite gets booleans as 0 and 1, PostgreSQL
// boolean columns get booleans whether the export came from SQLite
// (numbers) or PostgreSQL (booleans). Text, including times and UUIDs,
// passes through; both databases parse it into the column type.
func decodeValue(raw any, col dialect.Column, sqlite bool) (any, error) {
	switch x := raw.(type) {
	case nil:
		return nil, nil
	case bool:
		if sqlite {
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return x, nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			if col.Boolean() {
				return n != 0, nil
			}
			return n, nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", x)
		}
		return f, nil
	case string:
		if col.Boolean() {
			return nil, fmt.Errorf("text %q for a boolean column", x)
		}
		return x, nil
	case map[string]any:
		s, ok := x[bytesKey].(string)
		if !ok || len(x) != 1 {
			return nil, fmt.Errorf("unexpected object value")
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("invalid %s value: %w", bytesKey, err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", raw)
	}
}
