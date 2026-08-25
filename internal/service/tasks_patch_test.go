package service

import (
	"fmt"
	"reflect"
	"testing"
)

// isPositionOnly enumerates every field of TaskPatch by hand, so its
// correctness is a property of the whole struct rather than of its own body: a
// field added to TaskPatch and forgotten there makes a patch touching only that
// field read as a drag-reorder. A drag writes no activity row on purpose, so
// the consequence is not a compile error or a wrong sentence — it is a real
// edit silently missing from the log, which nobody notices until they go
// looking for it.
//
// This walks the struct rather than trusting the enumeration. It needs no
// database: it is the one test here that still runs when Postgres is not
// reachable, which is deliberate, because the mistake it catches is made while
// editing a type and not while running an integration suite.
func TestIsPositionOnlyCoversEveryTaskPatchField(t *testing.T) {
	typ := reflect.TypeOf(TaskPatch{})

	pos := "0m"
	if !(TaskPatch{Position: &pos}).isPositionOnly() {
		t.Fatal("a patch carrying only a position is not position-only — the sanity direction is broken")
	}

	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Name == "Position" {
			continue
		}
		patch := reflect.New(typ).Elem()
		patch.FieldByName("Position").Set(reflect.ValueOf(&pos))
		if err := setNonZeroForTest(patch.Field(i)); err != nil {
			t.Fatalf("TaskPatch.%s is a %s, which this test cannot set (%v).\n"+
				"Teach setNonZeroForTest that kind, AND add the field to TaskPatch.isPositionOnly "+
				"in internal/service/tasks.go.", field.Name, field.Type, err)
		}
		if patch.Interface().(TaskPatch).isPositionOnly() {
			t.Fatalf("a patch setting Position and %s reads as position-only.\n"+
				"Add %s to TaskPatch.isPositionOnly in internal/service/tasks.go. Until you do, an "+
				"update touching that field writes no activity row and vanishes from the log.",
				field.Name, field.Name)
		}
	}
}

// setNonZeroForTest gives a TaskPatch field a value isPositionOnly must reject.
// Only the kinds TaskPatch actually uses; anything else fails loudly above
// rather than passing by not being checked.
func setNonZeroForTest(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		return nil
	case reflect.Bool:
		v.SetBool(true)
		return nil
	default:
		return fmt.Errorf("unsupported kind %s", v.Kind())
	}
}
