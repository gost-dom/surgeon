// An important note about tests!
//
// Tests here generally start with a root object, perform surgery, and compare
// the entire dependency tree, or a branch against a glone created using
// [encoding/gob].
//
// It is imperative that the clone is created _before_ actually performing
// surgery, so any accidental mutation suring surgery doesn't result in false
// positives.

package surgeon_test

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"testing"

	"github.com/gost-dom/surgeon"
)

type SimpleDependency struct{}

type SimpleRoot struct {
	Dep *SimpleDependency
}

func NewSimpleRoot() SimpleRoot {
	return SimpleRoot{new(SimpleDependency)}
}

func cloneObject[T any](t *testing.T, instance T) (result T) {
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	dec := gob.NewDecoder(&buffer)
	if err := enc.Encode(instance); err != nil {
		t.Fatal("Encoding error", err)
	}
	if err := dec.Decode(&result); err != nil {
		t.Fatal("Decoding error", err)
	}
	return
}

func TestSurgeon(t *testing.T) {
	expected := NewSimpleRoot()

	analysis := surgeon.Analyse(cloneObject(t, expected))
	actual := analysis.Create()

	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("Clone was not identical to original instance")
	}
}
