package surgeon

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// A Scope identifies if a visited type in an object graph is in scope for
// further dependency analysis.
//
// Typically you want your own types in scopes, excluding std and 3rd party
// libraries.
type Scope interface{ InScope(reflect.Type) bool }

// PackagePrefixScope implements a [Scope] that selects all packages with a
// specific prefix. The intended use case it to pass the module package name.
type PackagePrefixScope string

func (s PackagePrefixScope) InScope(t reflect.Type) bool {
	return strings.HasPrefix(t.PkgPath(), string(s))
}

// Set this to true to make surgeon trigger explicit consistency checks and
// debug output.
var DiagnosticsMode bool

// Analyses a configured object. The resulting [Graph] can be used to
// replace dependencies.
//
// The types that will be analysed can be controlled by the scopes. If scopes
// are used, the visited type must be in scope of all scopes. A typical use case
// it to specify use the [PackagePrefixScope] to specify the root path of your
// package.
//
//	// Full package path: example.com/package/internal/server
//
//	package server
//
//	surgeon.BuildGraph(server, PackagePrefixScope("example.com/package"))
func BuildGraph[T any](instance T, scopes ...Scope) *Graph[T] {
	result := &Graph[T]{
		instance:     instance,
		scopes:       scopes,
		dependencies: newGraphDependencies(),
		interfaces:   make(map[reflect.Type]struct{}),
		values:       make(map[reflect.Type]graphValue),
	}

	v := reflect.ValueOf(result.instance)
	result.buildTypeDependencies(v, initializePointerStrategy[T]{result}, v, nil)

	return result
}

// builderStrategy controls how some dependencies are build.
//
// The first iteration did not attempt to build the root dependency graph, only
// replace dependencies for tests. It worked from the assumption that nil
// pointer values were an invalid value.
//
// As it was realised that the library could be used to build the real production
// runtime graph, a different strategy was adopted, to initialize nil pointer
// values to zero.
//
// The [defaultBuilderStrategy] represents the original use case, but is not
// used in the code. It is not certain both use cases have any value going
// forward, but allowing client to have control over how the graph is traversed
// does seem to make sense, to the idea of a strategy is kept in code, even if
// not visible to client code.
type builderStrategy interface {
	nilPointer(v reflect.Value, visitedTypes []reflect.Type) types
}

type defaultBuilderStrategy struct{}

func (s defaultBuilderStrategy) nilPointer(v reflect.Value) types {
	type_ := v.Type()
	panic(fmt.Sprintf("surgeon: nil value: %s", printType(type_)))
}

type initializePointerStrategy[T any] struct {
	graph *Graph[T]
}

func (i initializePointerStrategy[T]) nilPointer(v reflect.Value,
	visitedTypes []reflect.Type,
) types {
	type_ := v.Type()
	if existing, ok := i.graph.values[type_]; ok {
		v.Set(existing.value)
		i.graph.values[type_] = graphValue{v, false}
	} else {
		v.Set(reflect.New(type_.Elem()))
	}

	e := v.Elem()
	newInitValue := e
	tmp := i.graph.buildTypeDependencies(e, i, newInitValue, visitedTypes)
	tmp.append(type_)

	if init, ok := v.Interface().(Initer); ok && !i.graph.values[type_].initialized {
		init.Init()
	}
	i.graph.values[type_] = graphValue{v, true}
	return tmp
}

type types []reflect.Type

func (t *types) append(types ...reflect.Type) {
	if t == nil {
		panic("Appending to nil")
	}
	for _, type_ := range types {
		*t = append(*t, type_)
	}
}

func (t types) String() string {
	names := make([]string, len(t))
	for i, typ := range t {
		names[i] = printType(typ)
	}
	return strings.Join(names, ", ")
}

func mergeDeps[T any, U any](dst *Graph[T], src *Graph[U]) {
	dst.dependencies.merge(src.dependencies)
	for intf := range src.interfaces {
		dst.registerInterface(intf)
	}
}

func isPointer(t reflect.Type) bool { return t.Kind() == reflect.Pointer }

type debugInfo struct {
	Type reflect.Type
}

func getFieldDeps(graph graphDependencies, t reflect.Type, f reflect.StructField) types {
	var res types
	for tt, ff := range graph.get(t).All() {
		for _, field := range ff {
			if f.Name == field.Name {
				res.append(tt)
			}
		}
	}
	return res
}

// Initer is the interface for an Init() function. A type in the dependency
// graph that implement interface Initer will have its Init function called when
// cloned.
//
// A use case is when replacing a "use case" that a router/ServeMux calls. When cloning
// the type with the router, the router will still be configured to call methods
// on the source instance; not the new clone. The Init function should create a
// new router/ServeMux, setting up the routes, pointing to methods on the new
// cloned instance.
type Initer interface{ Init() }

func printType(t reflect.Type) string {
	if isPointer(t) {
		return "*" + printType(t.Elem())
	}
	return fmt.Sprintf("%s (%s)", t.Name(), t.PkgPath())
}

type kv[T any, U any] struct {
	k T
	v U
}

func iterateStringMapSorted[T any](types map[string]T) []kv[string, T] {
	keys := make([]string, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	res := make([]kv[string, T], len(keys))
	for i, k := range keys {
		res[i] = kv[string, T]{k, types[k]}
	}
	return res
}

func Debug[T any](a *Graph[T]) string {
	var b strings.Builder
	b.WriteString("Registered types:\n")
	for k, v := range a.dependencies.AllSorted() {
		fieldToDeps := make(map[string][]reflect.Type)
		b.WriteString(fmt.Sprintf(" - %s\n", k.Name()))
		b.WriteString("    By dependency\n")
		for k2, f := range v.AllSorted() {
			b.WriteString(fmt.Sprintf("     - %s (count: %d)\n", printType(k2), len(f)))
			for _, d := range f {
				n := d.Name
				fieldToDeps[n] = append(fieldToDeps[n], k2)
			}
		}
		b.WriteString("    By field\n")
		for _, kv := range iterateStringMapSorted(fieldToDeps) {
			n := kv.k
			deps := kv.v
			b.WriteString(fmt.Sprintf("    - %s\n", n))
			for _, d := range deps {
				b.WriteString(fmt.Sprintf("      - %s\n", printType(d)))
			}
		}
	}
	b.WriteString("Registered interfaces:\n")
	for t := range a.interfaces {
		b.WriteString(fmt.Sprintf("- %s\n", printType(t)))
	}
	return b.String()
}

func allDepsOfNewInstance(t reflect.Type, instance any, scopes []Scope) (types, *Graph[any]) {
	injectedType := reflect.TypeOf(instance)
	depGraph := BuildGraph(instance, scopes...)
	var allDeps []reflect.Type
	if isPointer(injectedType) {
		allDeps = append(allDeps, injectedType)
		injectedType = injectedType.Elem()
	}
	allDeps = append(allDeps, t)
	for dep, fields := range depGraph.dependencies.get(injectedType).All() {
		deps := make([]reflect.Type, len(fields))
		for i := range fields {
			deps[i] = dep
		}
		allDeps = append(allDeps, deps...)
	}
	allDeps = append(allDeps, injectedType)
	return allDeps, depGraph
}

// Create a new graph with a dependency replaced by a new implementation. Panics
// if the root object in the graph doesn't include the replaced type in the
// dependency tree. Panics if the replaced type isn't an interface.
func Replace[V any, T any](a *Graph[T], instance V) *Graph[T] {
	res := a.clone()
	t := reflect.TypeFor[V]()
	if t.Kind() != reflect.Interface {
		panic("surgeon: Replaced type must be an interface")
	}

	allDeps, depGraph := allDepsOfNewInstance(t, instance, a.scopes)

	replacedInstance, removedTypes, _ := res.replace(
		reflect.ValueOf(a.instance), reflect.ValueOf(instance),
		res.dependencies.clone(),
		t, true, allDeps, nil)
	for res.cleanTypes(removedTypes) {
	}
	mergeDeps(res, depGraph)

	res.instance = replacedInstance.Interface().(T)

	if DiagnosticsMode {
		expectedGraph := BuildGraph(res.instance, a.scopes...)
		if !reflect.DeepEqual(expectedGraph, res) {
			fmt.Println(
				"Objects identical: ",
				reflect.DeepEqual(expectedGraph.instance, res.instance),
			)
			fmt.Println(
				"Scopes identical: ",
				reflect.DeepEqual(expectedGraph.scopes, res.scopes),
			)
			fmt.Println(
				"Dependencies identical: ",
				reflect.DeepEqual(expectedGraph.dependencies, res.dependencies),
			)
			fmt.Println(
				"Interfaces identical: ",
				reflect.DeepEqual(expectedGraph.interfaces, res.interfaces),
			)
			panic(fmt.Sprintf(`  --- Surgeon inconsistency detected ---

## Expected graph:
%s

## Actual graph:
%s

## Original graph:
%s

## Graph of new dependency:
%s
	`, Debug(expectedGraph), Debug(res), Debug(a), Debug(depGraph)))
		}
	}
	return res
}

func ReplaceAll[T any](a *Graph[T], instance any) (res *Graph[T]) {
	res = a.clone()
	res.Inject(instance)
	return res
}

type IncompleteGraphError struct {
	str reflect.Type
	f   reflect.StructField
}

func (e IncompleteGraphError) Error() string {
	return fmt.Sprintf("uninitialized field: %s - type: %s", e.f.Name, e.str.Name())
}
