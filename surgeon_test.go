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
	"net/http"
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

	analysis := surgeon.BuildGraph(cloneObject(t, expected))
	actual := analysis.Instance()

	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("Clone was not identical to original instance")
	}
}

// Just a silly interface to test interface implementation
type Aer interface{ A() string }

// RealA silly type implementing interface Aer
type RealA struct{}

func (RealA) A() string { return "Real A" }

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
		Aer:        RealA{},
		SimpleRoot: simpleRoot,
	}

	subTreeCopy := cloneObject(t, tree.SimpleRoot)

	analysis := surgeon.BuildGraph(tree)
	actual := surgeon.Replace[Aer](analysis, FakeA{}).Instance()

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
		Aer:        RealA{},
		SimpleRoot: simpleRoot,
	}

	subTreeCopy := cloneObject(t, tree.SimpleRoot)

	analysis := surgeon.BuildGraph(tree)
	actual := surgeon.Replace[Aer](analysis, FakeA{}).Instance()

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
		"surgeon: replacing type Aer: no dependency in the graph",
		func() {
			a := surgeon.BuildGraph(tree)
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
		"surgeon: replacing type Aer: no dependency in the graph",
		func() {
			a := surgeon.BuildGraph(tree)
			surgeon.Replace[Aer](a, FakeA{})
		},
	)
}

// Just a silly interface to test interface implementation
type Ber interface{ B() string }

// A silly type implementing interface Aer
type RealB struct{ Aer Aer }

func (b RealB) B() string { return "B says: " + b.Aer.A() }

type TypeWithLayersOfAbstraction struct {
	Ber Ber
}

type FakeB struct{}

func (FakeB) B() string { return "Fake B" }

func TesReplaceInsidetLayersOfAbstractions(t *testing.T) {
	actual := TypeWithLayersOfAbstraction{Ber: RealB{Aer: RealA{}}}
	graph := surgeon.BuildGraph(actual)
	instance := surgeon.Replace[Aer](graph, FakeA{}).Instance()
	assert.Equal(t, "B says: FakeA", instance.Ber.B())
}

func TestReplaceFirstLayersOfAbstractions(t *testing.T) {
	actual := TypeWithLayersOfAbstraction{Ber: RealB{Aer: RealA{}}}
	graph := surgeon.BuildGraph(actual)
	graph = surgeon.Replace[Ber](graph, FakeB{})
	instance := graph.Instance()
	assert.Equal(t, "Fake B", instance.Ber.B())

	// There should no longer be an Aer in the graph
	assert.PanicsWithValue(t,
		"surgeon: replacing type Aer: no dependency in the graph",
		func() { surgeon.Replace[Aer](graph, RealA{}) })
}

type RootWithMultipleDepsToAer struct {
	Aer Aer
	Ber Ber
}

func TestReplaceDependencyInMultipleBranches(t *testing.T) {
	original := RootWithMultipleDepsToAer{RealA{}, RealB{RealA{}}}
	graph := surgeon.BuildGraph(original)

	// Replace A
	graphWithFakeA := surgeon.Replace[Aer](graph, &FakeA{})
	withAerReplaced := graphWithFakeA.Instance()
	assert.Equal(t, "Fake A", withAerReplaced.Aer.A())
	assert.Equal(t, "B says: Fake A", withAerReplaced.Ber.B())

	// Replace B after A
	withBerReplaced := surgeon.Replace[Ber](graphWithFakeA, &FakeB{}).Instance()
	assert.Equal(t, "Fake A", withBerReplaced.Aer.A())
	assert.Equal(t, "Fake B", withBerReplaced.Ber.B())
}

func TestReplaceDependencyInMultipleBranchesTopFirst(t *testing.T) {
	original := RootWithMultipleDepsToAer{RealA{}, RealB{RealA{}}}
	graph := surgeon.BuildGraph(original)

	// Replace B
	graphWithFakeB := surgeon.Replace[Ber](graph, &FakeB{})
	withBerReplaced := graphWithFakeB.Instance()
	assert.Equal(t, "Real A", withBerReplaced.Aer.A())
	assert.Equal(t, "Fake B", withBerReplaced.Ber.B())

	// Replace A after B
	withAerReplaced := surgeon.Replace[Aer](graphWithFakeB, &FakeA{}).Instance()
	assert.Equal(t, "Fake A", withAerReplaced.Aer.A())
	assert.Equal(t, "Fake B", withAerReplaced.Ber.B())
}

type Cer interface{ C() string }
type RealC struct{}

func (c RealC) C() string {
	return "Real C"
}

type FakeAandB struct{}

func (FakeAandB) A() string { return "FakeAandB.A" }
func (FakeAandB) B() string { return "FakeAandB.B" }

type RootWithDepsToABC struct {
	Aer Aer
	Ber Ber
	Cer Cer
}

func TestInjectingAllInterfacesFromType(t *testing.T) {
	root := &RootWithDepsToABC{
		Aer: RealA{},
		Ber: RealB{Aer: RealA{}},
		Cer: RealC{},
	}
	graph := surgeon.BuildGraph(root)
	actual := surgeon.ReplaceAll(graph, FakeAandB{}).Instance()

	assert.Equal(t, "FakeAandB.A", actual.Aer.A())
	assert.Equal(t, "FakeAandB.B", actual.Ber.B())
	assert.Equal(t, "Real C", actual.Cer.C())
}

func TestLimitGraphToKnownTypes(t *testing.T) {
	root := &struct{ Routes http.ServeMux }{}
	// The following panics without the scope, as it will try to iterate through
	// private members
	surgeon.BuildGraph(root)
}

func TestLimitGraphToExportedNames(t *testing.T) {
	root := &struct{ routes http.ServeMux }{}
	surgeon.BuildGraph(root)
}

type Initializable struct {
	InitCount int
	Callback  func()
}

func (i *Initializable) Init() {
	i.InitCount++
	if i.Callback != nil {
		i.Callback()
	}
}
func (i Initializable) Initialized() bool { return i.InitCount > 0 }

type InitializableA struct {
	RealA
	Initializable
}

type InitializableB struct {
	RealB
	Initializable
}

type InitializableBNonPointer struct {
	RealB
	*Initializable
}

type InitializableC struct {
	RealC
	Initializable
}

type InitializableRoot struct {
	Initializable
	A Aer
	B Ber
	C Cer
}

func TestReplacedDepenciesAreInitialized(t *testing.T) {
	A := &InitializableA{}
	B := &InitializableB{}
	B.Aer = A
	root := InitializableRoot{
		A: A,
		B: B,
		C: &InitializableC{},
	}
	graph := surgeon.BuildGraph(&root)
	cloned := surgeon.Replace[Aer](graph, FakeA{}).Instance()
	assert := assert.New(t)

	// Assert original
	assert.False(root.Initialized())
	assert.False(root.A.(*InitializableA).Initialized())
	assert.False(root.B.(*InitializableB).Initialized())
	assert.False(root.C.(*InitializableC).Initialized())

	// Assert clone
	assert.Equal(0, cloned.C.(*InitializableC).InitCount, "C init count")
	assert.Equal(1, cloned.B.(*InitializableB).InitCount, "B init count")
	assert.Equal(1, cloned.InitCount, "Root init count")
}

func TestReplacedDepenciesAreInitializedNonPointer(t *testing.T) {
	A := &InitializableA{}
	B := InitializableBNonPointer{}
	B.Initializable = &Initializable{}
	B.Aer = A
	root := InitializableRoot{
		A: A,
		B: B,
		C: &InitializableC{},
	}
	graph := surgeon.BuildGraph(&root)
	cloned := surgeon.Replace[Aer](graph, FakeA{}).Instance()
	assert := assert.New(t)

	// Assert clone
	assert.Equal(0, cloned.C.(*InitializableC).InitCount, "C init count")
	assert.Equal(1, cloned.B.(InitializableBNonPointer).InitCount, "B init count")
	assert.Equal(1, cloned.InitCount, "Root init count")
}

type InitializableWithDep struct {
	A Aer
	i Initializable
}

func (d *InitializableWithDep) Init() { d.i.Init() }

type RootWithEmbeddedInitializable struct {
	InitializableWithDep
	initCount int
}

func (i *RootWithEmbeddedInitializable) Init() {
	i.initCount++
}

func TestReplaceDependencyOfEmbeddedInitializable(t *testing.T) {
	// If an object embeds an initializable, it's still only the _root_ that
	// should be considered in the graph, and Init should be called on the
	// embedder. The embed's Init should only be called if
	// - The embedder doesn't provide an Init itself
	// - The embedder's Init calls the embedded Init
	root := RootWithEmbeddedInitializable{InitializableWithDep: InitializableWithDep{A: RealA{}}}
	graph := surgeon.BuildGraph(&root)
	clone := surgeon.Replace[Aer](graph, FakeA{}).Instance()

	assert.Equal(t, 1, clone.initCount, "Init called on root")
	assert.Equal(t, 0, clone.InitializableWithDep.i.InitCount, "Init called on embed")
}

type RootWithSimplInterfaceDependency struct {
	A Aer
	B Ber
}

func TestUseAsADIFramework(t *testing.T) {
	root := &RootWithSimplInterfaceDependency{B: new(RealB)}
	g := surgeon.BuildGraph(root)
	instance1 := surgeon.ReplaceAll(g, RealA{}).Instance()
	assert.Equal(t, "Real A", instance1.A.A())

	g = surgeon.ReplaceAll(g, RealB{})
	g = surgeon.ReplaceAll(g, FakeA{})
	instance2 := g.Instance()

	assert.Equal(t, "B says: Fake A", instance2.B.B())
}

func TestDIUpdatesUsedInterfaces(t *testing.T) {
	g := surgeon.BuildGraph(&RealB{})
	instance := surgeon.ReplaceAll(g, FakeA{}).Instance()
	assert.Equal(t, "B says: Fake A", instance.B())
}

func TestFulfillingADependencyWithANewDependency(t *testing.T) {
	var root = struct{ B Ber }{}
	g := surgeon.BuildGraph(&root)
	g = surgeon.Replace[Ber](g, RealB{})
	g = surgeon.Replace[Aer](g, RealA{})
	assert.Equal(t, "B says: Real A", g.Instance().B.B())
}

func TestFulfillingADependencyWithANewPointerDependency(t *testing.T) {
	var root = struct{ B Ber }{}
	g := surgeon.BuildGraph(&root)
	g = surgeon.Replace[Ber](g, &RealB{})
	g = surgeon.Replace[Aer](g, RealA{})
	assert.Equal(t, "B says: Real A", g.Instance().B.B())
}

type X struct{ B RealB }
type Y struct{ B RealB }
type XYRoot struct {
	X X
	Y Y
}

func TestFulfillingADependencyWithMultiplePaths(t *testing.T) {
	//t.SkipNow()
	var root = XYRoot{}
	g := surgeon.BuildGraph(&root)
	g = surgeon.Replace[Aer](g, RealA{})
	assert.Equal(t, "B says: Real A", g.Instance().X.B.B())
	assert.Equal(t, "B says: Real A", g.Instance().Y.B.B())
}

func init() {
	surgeon.DiagnosticsMode = true
}
