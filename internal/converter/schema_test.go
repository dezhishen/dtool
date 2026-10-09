package converter

import "testing"

func TestDetectType(t *testing.T) {
	cases := []struct {
		vals []string
		want string
	}{
		{[]string{"1", "2", "-3"}, "integer"},
		{[]string{"1", "2.5"}, "number"},
		{[]string{"00123", "456"}, "string"},
		{[]string{"1234567890123456"}, "number"},
		{[]string{"true", "False"}, "boolean"},
		{[]string{"2026-01-02", "2026-03-04"}, "date"},
		{[]string{"a", "1"}, "string"},
		{nil, "null"},
	}
	for _, c := range cases {
		if got, _ := detectType(c.vals); got != c.want {
			t.Errorf("detectType(%v) = %s, want %s", c.vals, got, c.want)
		}
	}
}

func TestInferSchemaEnumUnique(t *testing.T) {
	cols := [][]string{{"1", "2", "3"}, {"N", "S", "N"}, {"1", "", "3"}}
	s := InferSchema([]string{"id", "region", "amt"}, cols, 3)
	if !s[0].Unique || s[0].Nullable {
		t.Errorf("id: %+v", s[0])
	}
	if len(s[1].Enum) != 2 {
		t.Errorf("region enum: %+v", s[1])
	}
	if !s[2].Nullable || *s[2].Max != 3 {
		t.Errorf("amt: %+v", s[2])
	}
}
