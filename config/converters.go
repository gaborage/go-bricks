package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	maxInt = int(^uint(0) >> 1)
	minInt = -maxInt - 1

	// Error message constants for type conversion
	errMsgUnsupportedType         = "unsupported type %T"
	errMsgUnsupportedSignedType   = "unsupported signed int type %T"
	errMsgUnsupportedUnsignedType = "unsupported unsigned int type %T"
)

var (
	maxInt64ExactFloat = math.Nextafter(float64(math.MaxInt64), math.Inf(-1))
	minInt64ExactFloat = float64(math.MinInt64)

	// Error variables for simple messages without format specifiers
	errEmptyString = errors.New("empty string")
)

// toInt converts various types to int with overflow protection.
func toInt(value any) (int, error) {
	n, err := toInt64(value)
	if err != nil {
		return 0, err
	}
	if n > int64(maxInt) || n < int64(minInt) {
		return 0, fmt.Errorf("value %d overflows int", n)
	}
	return int(n), nil
}

// toInt64 converts various types to int64.
func toInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int, int8, int16, int32:
		return toInt64FromSignedInt(v)
	case uint, uint8, uint16, uint32, uint64:
		return toInt64FromUnsignedInt(v)
	case float32:
		return floatToInt64(float64(v))
	case float64:
		return floatToInt64(v)
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return 0, errEmptyString
		}
		return strconv.ParseInt(str, 10, strconv.IntSize)
	default:
		return 0, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

// toInt64FromSignedInt handles conversion from signed integer types
func toInt64FromSignedInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	default:
		return 0, fmt.Errorf(errMsgUnsupportedSignedType, value)
	}
}

// toInt64FromUnsignedInt handles conversion from unsigned integer types with overflow checks
func toInt64FromUnsignedInt(value any) (int64, error) {
	switch v := value.(type) {
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint:
		if uint64(v) > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("value %d overflows int64", v)
		}
		return int64(v), nil //#nosec G115 -- safe conversion after overflow check
	case uint64:
		if v > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("value %d overflows int64", v)
		}
		return int64(v), nil //#nosec G115 -- safe conversion after overflow check
	default:
		return 0, fmt.Errorf(errMsgUnsupportedUnsignedType, value)
	}
}

// toFloat64 converts various types to float64.
func toFloat64(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return 0, errEmptyString
		}
		return strconv.ParseFloat(str, 64)
	default:
		return 0, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

// toBool converts various types to bool.
func toBool(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return false, errEmptyString
		}
		b, err := strconv.ParseBool(str)
		if err != nil {
			return false, err
		}
		return b, nil
	case int, int8, int16, int32, int64:
		n, err := toInt64(v)
		if err != nil {
			return false, err
		}
		return n != 0, nil
	case uint, uint8, uint16, uint32, uint64:
		n, err := toInt64(v)
		if err != nil {
			return false, err
		}
		return n != 0, nil
	default:
		return false, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

// floatToInt64 converts float64 to int64 with validation.
func floatToInt64(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid float value")
	}
	if math.Trunc(value) != value {
		return 0, fmt.Errorf("value %v is not an integer", value)
	}
	if value > maxInt64ExactFloat || value < minInt64ExactFloat {
		return 0, fmt.Errorf("value %v overflows int64", value)
	}
	result := int64(value)
	if float64(result) != value {
		return 0, fmt.Errorf("value %v cannot be represented exactly as int64", value)
	}
	return result, nil
}
