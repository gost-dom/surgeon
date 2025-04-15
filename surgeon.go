package surgeon

import (
	"fmt"
	"maps"
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
	result := &Graph[T]{instance: instance, scopes: scopes}
	result.buildDependencies()
	return result
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

type graphDependency = map[reflect.Type][]reflect.StructField
type graphDependencies = map[reflect.Type]graphDependency

// The Graph is the result of analysing a real object graph.
type Graph[T any] struct {
	instance T
	// First key is a type that _has_ dependencies, the "dependee".
	// The inner map is a map of all direct and indirect dependencies to the
	// fields on the dependee that has this as a dependecy.
	// This means, when replacing dependency of type A on an instance, we look
	// up the instance's type in the first map. Then we lookup the dependency
	// type (A) in the second map, which tells which fields in the instance that
	// has either a direct or indirect dependency to A.
	dependencies map[reflect.Type]map[reflect.Type][]reflect.StructField
	// interfaces contains a list of known interface types in the dependency
	// graph.
	interfaces map[reflect.Type]struct{}
	// scopes define which types should be analysed in the graph. Generally you
	// want to add your root scope.
	scopes []Scope
}

func mergeDeps[T any, U any](dst *Graph[T], src *Graph[U]) {
	for k, v := range src.dependencies {
		if dst2, ok := dst.dependencies[k]; !ok {
			dst.dependencies[k] = v
		} else {
			for k2, v2 := range v {
				if _, ok := dst2[k2]; ok {
					dst2[k2] = append(dst2[k2], v2...)
				} else {
					dst2[k2] = v2
				}
			}
		}
	}
	for intf := range src.interfaces {
		dst.registerInterface(intf)
	}
}

func (a *Graph[T]) buildDependencies() {
	a.dependencies = make(map[reflect.Type]map[reflect.Type][]reflect.StructField)
	a.interfaces = make(map[reflect.Type]struct{})
	v := reflect.ValueOf(a.instance)
	a.buildTypeDependencies(v, v, nil)
}

func (a *Graph[T]) inScope(t reflect.Type) bool {
	if len(a.scopes) == 0 {
		return true
	}
	for _, s := range a.scopes {
		if s.InScope(t) {
			return true
		}
	}
	return false
}

func isPointer(t reflect.Type) bool { return t.Kind() == reflect.Pointer }

func (g *Graph[T]) registerInterface(intfType reflect.Type) {
	g.interfaces[intfType] = struct{}{}
}
func (g *Graph[T]) removeInterface(intfType reflect.Type) {
	delete(g.interfaces, intfType)
}

// Builds the dependency graph, and potentially initializes objects.
// - v is object being iterated for the dependency graph
// - initValue is the value that should be initialized (if enabled).
// - visitedTypes is for troubleshooting the code only.
//
// The result is all types that the value depends on directly or indirectly. The
// same type can have multiple entries, if a value has multiple paths to that
// dependency. A likely example would be a database connection pool.
func (a *Graph[T]) buildTypeDependencies(
	v reflect.Value,
	initValue reflect.Value,
	visitedTypes []reflect.Type,
) types {
	type_ := v.Type()
	if !a.inScope(type_) && !isPointer(type_) {
		return nil
	}
	if slices.Contains(visitedTypes, type_) {
		names := make([]string, len(visitedTypes)+1)
		for i, t := range visitedTypes {
			names[i] = t.Name()
		}
		names[len(visitedTypes)] = type_.Name()
		panic("surgeon: cyclic dependencies in graph: " + strings.Join(names, ", "))
	}
	visitedTypes = append(visitedTypes, type_)

	switch type_.Kind() {
	case reflect.Interface:
		a.registerInterface(type_)
		if v.IsZero() {
			return types{type_}
			// panic(fmt.Sprintf("surgeon: Value for %s (%s) is nil", type_.Name(), type_.PkgPath()))
		}
		fallthrough
	case reflect.Pointer:
		if v.IsZero() {
			panic(fmt.Sprintf("surgeon: nil value: %s", printType(type_)))
		}
		e := v.Elem()
		newInitValue := e
		if v != initValue {
			newInitValue = initValue
		}
		tmp := a.buildTypeDependencies(e, newInitValue, visitedTypes)
		tmp.append(type_)
		return tmp
	case reflect.Struct:
		var dependencies types
		typeDependencies := make(map[reflect.Type][]reflect.StructField)
		for _, f := range reflect.VisibleFields(type_) {
			if !f.IsExported() {
				continue
			}
			if len(f.Index) > 1 {
				continue
			}
			fieldValue := v.FieldByIndex(f.Index)
			initValue := fieldValue

			// If the current field is anonymous and implement the `Initer`
			// interface; it should not be called; as _this type_ also
			// implements `Initer`, and it's _that_ implementation that should
			// be called. This could potentially lead to an invalid
			// initialization due to either calling a function that shouldn't be
			// called, or calling Init twice.
			if f.Anonymous {
				initValue = v
			}
			fieldDependencies := a.buildTypeDependencies(fieldValue, initValue, visitedTypes)
			dependencies.append(fieldDependencies...)
			for _, depType := range fieldDependencies {
				typeDependencies[depType] = append(typeDependencies[depType], f)
			}
		}
		a.dependencies[type_] = typeDependencies
		dependencies.append(type_)
		return dependencies
	}
	return nil
}

func (a *Graph[T]) Instance() T {
	return a.instance
}

type debugInfo struct {
	Type reflect.Type
}

func (g *Graph[T]) cleanTypes(types []reflect.Type) (res bool) {
	for _, t := range types {
		found := false
		for _, v := range g.dependencies {
			v2, f := v[t]
			if len(v2) == 0 {
				if f {
					delete(v, t)
				}
			} else {
				found = true
			}
		}
		if !found {
			g.removeInterface(t)
			if g.dependencies[t] != nil {
				delete(g.dependencies, t)
				res = true
			}
		}
	}
	return
}

// replace rebuilds the parts of the graph in graphObj by replacing dependencies
// of type t with the value newValue.
//
// Panics if the dependency doesn't exist in the graph. If this scenario is
// detected on anything but the root, this is a bug in surgeon. isRoot is used
// only to create a helpful error message, "It's not you, it's us!".
func (a *Graph[T]) replace(
	graphObj, newValue reflect.Value,
	type_ reflect.Type,
	isRoot bool,
	replacedDeps []reflect.Type,
	stack []debugInfo,
) (reflect.Value, types) {
	objType := graphObj.Type()
	stack = append(stack, debugInfo{Type: objType})

	isInterface := objType.Kind() == reflect.Interface
	if isInterface {
		graphObj = graphObj.Elem()
		objType = graphObj.Type()
	}
	thisIsPointer := objType.Kind() == reflect.Pointer
	if thisIsPointer {
		graphObj = graphObj.Elem()
		objType = graphObj.Type()
	}

	deps := a.dependencies[objType]
	fieldsToUpdate := deps[type_]
	if len(fieldsToUpdate) == 0 {
		if isRoot {
			msg := fmt.Sprintf(
				"surgeon: replacing type %s: no dependency in the graph",
				type_.Name(),
			)
			panic(msg)
		} else {
			var stackInfo strings.Builder
			for _, stack := range stack {
				stackInfo.WriteString(fmt.Sprintf("  - Type: %s\n", printType(stack.Type)))
			}
			msg := fmt.Sprintf(
				"surgeon: Dependency tree is in an inconsistent state %s has no dependency to %s.\n- Please submit an issue at %s\n- Type replace type stack: \n%s",
				printType(objType), printType(type_), newIssueUrl, stackInfo.String(),
			)
			panic(msg)
		}
	}

	objCopyPtr := reflect.New(objType)
	objCopy := reflect.Indirect(objCopyPtr)
	objCopy.Set(graphObj)

	var depsRemoved types
	var handledFields []string
	for _, f := range fieldsToUpdate {
		if slices.Contains(handledFields, f.Name) {
			continue
		}
		handledFields = append(handledFields, f.Name)
		fieldValue := objCopy.FieldByIndex(f.Index)
		var depsRemovedInIteration types
		if f.Type == type_ {
			if !fieldValue.IsZero() {
				elemType := fieldValue.Elem().Type()
				depsRemovedInIteration = a.getDependencyTypes(elemType)
				depsRemovedInIteration.append(elemType)
				if isPointer(elemType) {
					depsRemovedInIteration.append(elemType.Elem())
				}
			}
			fieldValue.Set(newValue)
		} else {
			var v reflect.Value
			v, depsRemovedInIteration = a.replace(fieldValue, newValue, type_, false, replacedDeps, stack)
			fieldValue.Set(v)
		}
		for _, d := range depsRemovedInIteration {
			depFields := deps[d]
			idx := slices.IndexFunc(
				depFields,
				func(x reflect.StructField) bool { return x.Name == f.Name },
			)
			if idx == -1 {
				panic("Bad field")
			}
			deps[d] = slices.Delete(depFields, idx, idx+1)
		}
		for _, newDep := range replacedDeps {
			fields := deps[newDep]
			fields = append(fields, f)
			deps[newDep] = fields
		}

		depsRemoved = append(depsRemoved, depsRemovedInIteration...)
	}

	var result reflect.Value
	if thisIsPointer {
		result = objCopyPtr
	} else {
		result = objCopy
	}
	if i, ok := result.Interface().(Initer); ok {
		i.Init()
	}
	return result, depsRemoved
}

type Initer interface{ Init() }

func (a *Graph[T]) getDependencyTypes(t reflect.Type) types {
	var res types
	// pointer type dependencies are not registered
	if isPointer(t) {
		t = t.Elem()
	}
	for k, v := range a.dependencies[t] {
		for range v {
			res = append(res, k)
		}
	}
	return res
}

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
func iterateTypesSorted[T any](types map[reflect.Type]T) []kv[reflect.Type, T] {
	keys := make([]reflect.Type, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	slices.SortFunc(
		keys,
		func(x, y reflect.Type) int { return strings.Compare(x.Name(), y.Name()) },
	)
	res := make([]kv[reflect.Type, T], len(keys))
	for i, k := range keys {
		res[i] = kv[reflect.Type, T]{k, types[k]}
	}
	return res
}

func Debug[T any](a *Graph[T]) string {
	var b strings.Builder
	b.WriteString("Registered types:\n")
	for _, kv := range iterateTypesSorted(a.dependencies) {
		k := kv.k
		v := kv.v
		fieldToDeps := make(map[string][]reflect.Type)
		b.WriteString(fmt.Sprintf(" - %s\n", k.Name()))
		b.WriteString("    By dependency\n")
		for _, kv2 := range iterateTypesSorted(v) {
			k2 := kv2.k
			f := kv2.v
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

func cloneDependencies(d graphDependencies) graphDependencies {
	res := make(graphDependencies)
	for k, v := range d {
		d := make(graphDependency)
		maps.Copy(d, v)
		res[k] = d
	}
	return res
}

func (g *Graph[T]) clone() *Graph[T] {
	return &Graph[T]{
		g.instance,
		cloneDependencies(g.dependencies),
		g.interfaces,
		g.scopes,
	}
}

func allDepsOfNewInstance(instance any, scopes []Scope) (types, *Graph[any]) {
	injectedType := reflect.TypeOf(instance)
	var allDeps []reflect.Type
	depGraph := BuildGraph[any](instance, scopes...)
	for dep, fields := range depGraph.dependencies[injectedType] {
		deps := make([]reflect.Type, len(fields))
		for i := range fields {
			deps[i] = dep
		}
		allDeps = append(allDeps, deps...)
	}
	allDeps = append(allDeps, injectedType)
	if isPointer(injectedType) {
		allDeps = append(allDeps, injectedType.Elem())
	}
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

	allDeps, depGraph := allDepsOfNewInstance(instance, a.scopes)

	replacedInstance, removedTypes := res.replace(
		reflect.ValueOf(a.instance), reflect.ValueOf(instance), t, true, allDeps, nil)
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

// Inject injects a dependency into an existing graph. This is a mutating
// operation. Dependencies are replaced in the same was as ReplaceAll
//
// The intended use is building the object graph at application startup. When
// replacing dependencies in testing, use Replace or ReplaceAll
func (g *Graph[T]) Inject(instance any) {
	allDeps, depGraph := allDepsOfNewInstance(instance, g.scopes)
	t := reflect.TypeOf(instance)
	var interfaces []reflect.Type
	for i := range g.interfaces {
		if t.AssignableTo(i) {
			interfaces = append(interfaces, i)
		}
	}
	if len(interfaces) == 0 {
		panic(
			fmt.Sprintf(
				"surgeon: Graph.Inject: Instance of type %s does not implement an interface in the dependency graph of %s (has this already been replaced in this graph?)",
				t.Name(),
				reflect.TypeFor[T]().Name(),
			),
		)
	}
	for _, i := range interfaces {
		v, removedTypes := g.replace(
			reflect.ValueOf(g.instance),
			reflect.ValueOf(instance),
			i,
			true,
			allDeps,
			nil,
		)
		g.instance = v.Interface().(T)
		for g.cleanTypes(removedTypes) {
		}
	}
	mergeDeps(g, depGraph)
}

// Replace replaces a single dependency of the graph, and returns a partial
// clone. Any object in the graph that doesn't have a dependency is untouched,
// other objects are duplicated, and reinitialized.
//
// The intended use is for testing, where each test case needs its own clone for
// test isolation.
func (g *Graph[T]) ReplaceAll(instance any) *Graph[T] {
	res := g.clone()
	res.Inject(instance)
	return res
}

func ReplaceAll[T any](a *Graph[T], instance any) (res *Graph[T]) {
	res = a.clone()
	res.Inject(instance)
	return res
}
