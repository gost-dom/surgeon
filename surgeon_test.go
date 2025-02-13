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
	"github.com/stretchr/testify/assert"
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

// Just a silly interface to test interface implementation
type Aer interface{ A() string }

// A silly type implementing interface Aer
type A struct{}

func (A) A() string { return "Real A" }

type FakeA struct{}

func (FakeA) A() string { return "Fake A" }

type RootWithSingleInterfaceDependencyAndSubtree struct {
	Aer        Aer
	SimpleRoot *SimpleRoot
}

func TestReplaceSingleDependencyOnPointer(t *testing.T) {
	tmp := NewSimpleRoot()
	simpleRoot := &tmp
	tree := &RootWithSingleInterfaceDependencyAndSubtree{
		Aer:        A{},
		SimpleRoot: simpleRoot,
	}

	subTreeCopy := cloneObject(t, tree.SimpleRoot)

	analysis := surgeon.Analyse(tree)
	actual := surgeon.Replace[Aer](analysis, FakeA{}).Create()

	assert.Equal(t, "Fake A", actual.Aer.A())
	if !reflect.DeepEqual(actual.SimpleRoot, subTreeCopy) {
		t.Fatal("Object tree was not equal")
	}
	assert.Same(t, simpleRoot, actual.SimpleRoot, "It's pointing somewhere else")
}

func TestReplaceSingleDependencyOnNonPointer(t *testing.T) {
	tmp := NewSimpleRoot()
	simpleRoot := &tmp
	tree := RootWithSingleInterfaceDependencyAndSubtree{
		Aer:        A{},
		SimpleRoot: simpleRoot,
	}

	subTreeCopy := cloneObject(t, tree.SimpleRoot)

	analysis := surgeon.Analyse(tree)
	actual := surgeon.Replace[Aer](analysis, FakeA{}).Create()

	assert.Equal(t, "Fake A", actual.Aer.A())
	if !reflect.DeepEqual(actual.SimpleRoot, subTreeCopy) {
		t.Fatal("Object tree was not equal")
	}
	assert.Same(t, simpleRoot, actual.SimpleRoot, "It's pointing somewhere else")
}
