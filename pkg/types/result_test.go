package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRowKeepsColumnOrder(t *testing.T) {
	r := Row{Columns: []string{"z", "a", "m"}, Values: []any{1, "x", nil}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"z":1,"a":"x","m":null}` {
		t.Fatalf("got %s", b)
	}
	var back Row
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(back.Columns) != "[z a m]" {
		t.Fatalf("columns = %v", back.Columns)
	}
	if v, ok := back.Get("z"); !ok || v.(json.Number).String() != "1" {
		t.Fatalf("z = %#v", v)
	}
	if _, ok := back.Get("nope"); ok {
		t.Fatal("unexpected column")
	}
}

func TestRowsArrayRoundTripAndNull(t *testing.T) {
	var rows []Row
	if err := json.Unmarshal([]byte(`[{"b":1,"a":2},null,{"c":"x"}]`), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Columns[0] != "b" || len(rows[1].Columns) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if err := json.Unmarshal([]byte(`[1]`), &rows); err == nil {
		t.Fatal("non-object row accepted")
	}
}

func TestAsError(t *testing.T) {
	te := Errorf(CodeNotFound, "x %d", 1).WithDetail("d").WithHint("h")
	wrapped := fmt.Errorf("wrap: %w", te)
	if got := AsError(wrapped, CodeGeneral); got != te || got.Code != CodeNotFound {
		t.Fatalf("got %+v", got)
	}
	if got := AsError(errors.New("boom"), CodeUsage); got.Code != CodeUsage || got.Message != "boom" {
		t.Fatalf("got %+v", got)
	}
	resp := te.Response()
	if resp.Error != "x 1" || resp.Detail != "d" || resp.Hint != "h" || resp.Code != CodeNotFound {
		t.Fatalf("resp = %+v", resp)
	}
}
