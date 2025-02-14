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

func TestPanicWhenDependencyDoesntExist(t *testing.T) {
	// If you try to replace a dependency that doesn't exist in the graph,
	// you have certain assumptions about the graph that aren't true, most
	// likely resulting in a false test outcome.
	// Surgeon will panic in this case, helping you identify the issue by
	// failing early.

	tree := NewSimpleRoot()
	assert.PanicsWithValue(
		t,
		"surgeon: Cannot replace type Aer. No dependency in the graph",
		func() {
			a := surgeon.Analyse(tree)
			surgeon.Replace[Aer](a, FakeA{})
		},
	)
}

func TestReplaceingBetweenLayersOfInterfaces(t *testing.T) {
	// If you try to replace a dependency that doesn't exist in the graph,
	// you have certain assumptions about the graph that aren't true, most
	// likely resulting in a false test outcome.
	// Surgeon will panic in this case, helping you identify the issue by
	// failing early.

	tree := NewSimpleRoot()
	assert.PanicsWithValue(
		t,
		"surgeon: Cannot replace type Aer. No dependency in the graph",
		func() {
			a := surgeon.Analyse(tree)
			surgeon.Replace[Aer](a, FakeA{})
		},
	)
}

// Just a silly interface to test interface implementation
type Ber interface{ B() string }

// A silly type implementing interface Aer
type B struct{ Aer Aer }

func (b B) B() string { return "B says: " + b.Aer.A() }

type TypeWithLayersOfAbstraction struct {
	Ber Ber
}

type FakeB struct{}

func (FakeB) B() string { return "Fake B" }

func TesReplaceInsidetLayersOfAbstractions(t *testing.T) {
	actual := TypeWithLayersOfAbstraction{Ber: B{Aer: A{}}}
	graph := surgeon.Analyse(actual)
	instance := surgeon.Replace[Aer](graph, FakeA{}).Create()
	assert.Equal(t, "B says: FakeA", instance.Ber.B())
}

func TestReplaceFirstLayersOfAbstractions(t *testing.T) {
	actual := TypeWithLayersOfAbstraction{Ber: B{Aer: A{}}}
	graph := surgeon.Analyse(actual)
	graph = surgeon.Replace[Ber](graph, FakeB{})
	instance := graph.Create()
	assert.Equal(t, "Fake B", instance.Ber.B())

	// There should no longer be an Aer in the graph
	assert.PanicsWithValue(t,
		"surgeon: Cannot replace type Aer. No dependency in the graph",
		func() { surgeon.Replace[Aer](graph, A{}) })
}

type RootWithMultipleDepsToAer struct {
	Aer Aer
	Ber Ber
}

func TestReplaceDependencyInMultipleBranches(t *testing.T) {
	original := RootWithMultipleDepsToAer{A{}, B{A{}}}
	graph := surgeon.Analyse(original)

	// Replace A
	withAerReplaced := surgeon.Replace[Aer](graph, &FakeA{}).Create()
	assert.Equal(t, "Fake A", withAerReplaced.Aer.A())
}
