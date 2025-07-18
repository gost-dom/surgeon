package surgeon

import (
	"errors"
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
	result := &Graph[T]{
		instance:     instance,
		scopes:       scopes,
		dependencies: newGraphDependencies(),
		interfaces:   make(map[reflect.Type]struct{}),
	}

	v := reflect.ValueOf(result.instance)
	result.buildTypeDependencies(v, v, nil, true)

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
	dependencies graphDependencies
	// interfaces contains a list of known interface types in the dependency
	// graph.
	interfaces map[reflect.Type]struct{}
	// scopes define which types should be analysed in the graph. Generally you
	// want to add your root scope.
	scopes []Scope
}

func mergeDeps[T any, U any](dst *Graph[T], src *Graph[U]) {
	dst.dependencies.merge(src.dependencies)
	for intf := range src.interfaces {
		dst.registerInterface(intf)
	}
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
	if _, found := g.interfaces[intfType]; found {
		delete(g.interfaces, intfType)
	}
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
	allowNil bool,
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
			if allowNil {
				return types{type_}
			}
			panic(fmt.Sprintf("surgeon: Value for %s (%s) is nil", type_.Name(), type_.PkgPath()))
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
		tmp := a.buildTypeDependencies(e, newInitValue, visitedTypes, allowNil)
		tmp.append(type_)
		return tmp
	case reflect.Struct:
		var dependencies types
		typeDependencies := newGraphDependency()
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
			fieldDependencies := a.buildTypeDependencies(
				fieldValue,
				initValue,
				visitedTypes,
				allowNil,
			)
			dependencies.append(fieldDependencies...)
			for _, depType := range fieldDependencies {
				typeDependencies.append(depType, f)
			}
		}
		a.dependencies.set(type_, typeDependencies)
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
		for _, v := range g.dependencies.All() {
			v2 := v.get(t)
			if len(v2) == 0 {
				v.delete(t)
			} else {
				found = true
			}
		}
		if !found {
			g.removeInterface(t)
			if g.dependencies.get(t) != nil {
				g.dependencies.delete(t)
				res = true
			}
		}
	}
	return
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

func (a *Graph[T]) setFieldDeps(t reflect.Type, deps types, field reflect.StructField) {
	oldDeps := a.dependencies.get(t)
	for k, v := range oldDeps.All() {
		oldDeps.set(k, slices.DeleteFunc(
			v,
			func(x reflect.StructField) bool { return x.Name == field.Name },
		))
	}
	for _, dep := range deps {
		oldDeps.append(dep, field)
	}
}

// replace rebuilds the parts of the graph in graphObj by replacing dependencies
// of type t with the value newValue.
//
// Panics if the dependency doesn't exist in the graph. If this scenario is
// detected on anything but the root, this is a bug in surgeon. isRoot is used
// only to create a helpful error message, "It's not you, it's us!".
func (a *Graph[T]) replace(
	graphObj, newValue reflect.Value,
	orgDeps graphDependencies,
	type_ reflect.Type,
	isRoot bool,
	replacedDeps []reflect.Type,
	stack []debugInfo,
) (reflect.Value, types, types) {
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

	deps := a.dependencies.get(objType)
	ts := deps.get(type_)
	fieldsToUpdate := slices.Clone(ts)
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
	var depsAdded types
	var handledFields []string
	for _, f := range fieldsToUpdate {
		if slices.Contains(handledFields, f.Name) {
			continue
		}
		handledFields = append(handledFields, f.Name)
		fieldValue := objCopy.FieldByIndex(f.Index)
		var depsRemovedInIteration types
		var depsAddedInIteration types
		if f.Type == type_ {
			depsRemovedInIteration = getFieldDeps(orgDeps, objType, f)
			depsAddedInIteration = replacedDeps
			a.setFieldDeps(objType, replacedDeps, f)
			fieldValue.Set(newValue)
		} else {
			var v reflect.Value
			v, depsRemovedInIteration, depsAddedInIteration = a.replace(fieldValue, newValue, orgDeps, type_, false, replacedDeps, stack)
			fieldValue.Set(v)
			for _, newDep := range replacedDeps {
				deps.append(newDep, f)
			}
			for _, d := range depsRemovedInIteration {
				depFields := deps.get(d)
				idx := slices.IndexFunc(
					depFields,
					func(x reflect.StructField) bool { return x.Name == f.Name },
				)
				if idx == -1 {
					panic(
						fmt.Sprintf(
							"Bad field: %s (%s) - %#v",
							f.Name,
							printType(objType),
							f,
						),
					)
				}
				deps.set(d, slices.Delete(depFields, idx, idx+1))
			}
		}

		depsRemoved = append(depsRemoved, depsRemovedInIteration...)
		depsAdded = append(depsAdded, depsAddedInIteration...)
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
	return result, depsRemoved, depsAdded
}

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

func (g *Graph[T]) clone() *Graph[T] {
	return &Graph[T]{
		g.instance,
		g.dependencies.clone(),
		maps.Clone(g.interfaces),
		g.scopes,
	}
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
	// fmt.Println("New deps to inject", allDeps)

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

// Inject injects a dependency into an existing graph. This is a mutating
// operation. Dependencies are replaced in the same was as ReplaceAll
//
// The intended use is building the object graph at application startup. When
// replacing dependencies in testing, use Replace or ReplaceAll
func (g *Graph[T]) Inject(instance any) {
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
		allDeps, depGraph := allDepsOfNewInstance(i, instance, g.scopes)
		v, removedTypes, _ := g.replace(
			reflect.ValueOf(g.instance),
			reflect.ValueOf(instance),
			g.dependencies.clone(),
			i,
			true,
			allDeps,
			nil,
		)
		for g.cleanTypes(removedTypes) {
		}
		mergeDeps(g, depGraph)
		g.instance = v.Interface().(T)
	}
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

type IncompleteGraphError struct {
	str reflect.Type
	f   reflect.StructField
}

func (e IncompleteGraphError) Error() string {
	return fmt.Sprintf("uninitialized field: %s - type: %s", e.f.Name, e.str.Name())
}

func (g *Graph[T]) validate(v reflect.Value) []error {
	t := v.Type()
	if !g.inScope(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Interface:
		return nil
	case reflect.Pointer:
		return g.validate(v.Elem())
	case reflect.Struct:
	default:
		return nil
	}

	n := t.NumField()
	errs := make([]error, 0, n)
	for i := range t.NumField() {
		f := t.Field(i)

		valid := f.IsExported() && g.inScope(f.Type)
		if !valid {
			continue
		}

		fv := v.Field(i)
		switch f.Type.Kind() {
		case reflect.Interface, reflect.Pointer:
			if fv.IsZero() {
				errs = append(errs, IncompleteGraphError{
					str: t,
					f:   f,
				})
			} else {
				e := g.validate(fv.Elem())
				errs = append(errs, e...)
			}
		case reflect.Struct:
			e := g.validate(fv)
			errs = append(errs, e...)
		}

	}
	return errs
}

// Validate checks that the graph is fully initialized.
//
// The intended use case is that the graph itself consists of objects that are
// not modified during the lifetime of the application, so the assumption is
// that values should not be nil. As such, any nil pointer or interface will
// result in an invalid graph.
//
// There is a reasonable use case for nil values, when part of the graph is used
// in different executables, e.g., a web server and CLI for the same application
// may reuse parts of the graph, but
//
// Please submit an issue if you have this use case (the solution would be to
// identify an interface where an object in the graph can tell if it's valid or
// not, and non-vil values is reduced to the _default_ behaviour)
func (g *Graph[T]) Validate() error {
	return errors.Join(g.validate(reflect.ValueOf(g.instance))...)
}

func ReplaceAll[T any](a *Graph[T], instance any) (res *Graph[T]) {
	res = a.clone()
	res.Inject(instance)
	return res
}
