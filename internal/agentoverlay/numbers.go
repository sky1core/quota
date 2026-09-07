package agentoverlay

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func normalizeForTOML(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		rat, ok := new(big.Rat).SetString(x.String())
		if !ok {
			return nil, fmt.Errorf("invalid number %q", x)
		}
		if rat.IsInt() && rat.Num().IsInt64() {
			return rat.Num().Int64(), nil
		}
		if !strings.ContainsAny(x.String(), ".eE") {
			return nil, fmt.Errorf("integer %s is outside the TOML int64 range", x)
		}
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil || math.IsInf(f, 0) || (f == 0 && rat.Sign() != 0) {
			return nil, fmt.Errorf("number %s is outside the TOML float64 range", x)
		}
		return f, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("non-finite number")
		}
		return normalizeForTOML(json.Number(strconv.FormatFloat(x, 'g', -1, 64)))
	case int, int64, bool, string:
		return x, nil
	case []any:
		result := make([]any, len(x))
		for i, item := range x {
			n, err := normalizeForTOML(item)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			result[i] = n
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(x))
		for _, key := range sortedKeys(x) {
			n, err := normalizeForTOML(x[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = n
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported TOML value %T", v)
	}
}
