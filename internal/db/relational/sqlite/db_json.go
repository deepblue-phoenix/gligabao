package sqlite

import (
	"encoding/json"
	"time"
)

//---------------------------------------------------------------------------------------------
// JSON column helpers

// jsonObject marshals m as a JSON object ("{}" for nil).
func jsonObject(m map[string]any) string {
	if m == nil {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseObject(s string) map[string]any {
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func jsonIDs(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

func parseIDs(s string) []int64 {
	ids := []int64{}
	if err := json.Unmarshal([]byte(s), &ids); err != nil || ids == nil {
		return []int64{}
	}
	return ids
}

func parseStoredTime(s string) (t time.Time) {
	if p := ParseTime(s); p != nil {
		return *p
	}
	return
}
