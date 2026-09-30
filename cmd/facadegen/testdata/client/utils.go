package camundaapi

import "time"

func PtrString(v string) *string { return &v }

type NullableTime struct {
	value *time.Time
}

// NewNullableTime wraps a time that may be unset.
func NewNullableTime(val *time.Time) *NullableTime { return &NullableTime{value: val} }
