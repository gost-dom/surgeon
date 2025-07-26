package surgeon

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
)

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

func (g *Graph[T]) inScope(t reflect.Type) bool {
	if len(g.scopes) == 0 {
		return true
	}
	if t.Kind() == reflect.Pointer {
		// If the type is a pointer, check if the type pointed to is in scope.
		t = t.Elem()
	}
	for _, s := range g.scopes {
		if s.InScope(t) {
			return true
		}
	}
	return false
}

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
func (g *Graph[T]) buildTypeDependencies(
	v reflect.Value,
	s builderStrategy,
	initValue reflect.Value,
	visitedTypes []reflect.Type,
) types {
	type_ := v.Type()
	if !g.inScope(type_) {
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
		g.registerInterface(type_)
		if v.IsZero() {
			return types{type_}
		}
		fallthrough
	case reflect.Pointer:
		if v.IsZero() {
			return s.nilPointer(v, visitedTypes)
		}
		e := v.Elem()
		newInitValue := e
		if v != initValue {
			newInitValue = initValue
		}
		tmp := g.buildTypeDependencies(e, s, newInitValue, visitedTypes)
		tmp.append(type_)
		return tmp
	case reflect.Struct:
		var dependencies types
		typeDependencies := newGraphDependency()
		for f, fieldValue := range g.inScopeFields(v) {
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
			fieldDependencies := g.buildTypeDependencies(
				fieldValue,
				s,
				initValue,
				visitedTypes,
			)
			dependencies.append(fieldDependencies...)
			for _, depType := range fieldDependencies {
				typeDependencies.append(depType, f)
			}
		}
		g.dependencies.set(type_, typeDependencies)
		dependencies.append(type_)
		return dependencies
	}
	return nil
}

func (g *Graph[T]) Instance() T {
	return g.instance
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

func (g *Graph[T]) setFieldDeps(t reflect.Type, deps types, field reflect.StructField) {
	oldDeps := g.dependencies.get(t)
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
func (g *Graph[T]) replace(
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

	deps := g.dependencies.get(objType)
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
			g.setFieldDeps(objType, replacedDeps, f)
			fieldValue.Set(newValue)
		} else {
			var v reflect.Value
			v, depsRemovedInIteration, depsAddedInIteration = g.replace(fieldValue, newValue, orgDeps, type_, false, replacedDeps, stack)
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

func (g *Graph[T]) clone() *Graph[T] {
	return &Graph[T]{
		g.instance,
		g.dependencies.clone(),
		maps.Clone(g.interfaces),
		g.scopes,
	}
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

// Validate checks that the graph is fully initialized.
//
// The intended use case is that the graph itself consists of objects that are
// not modified during the lifetime of the application. For this reason, it's
// assumed that values in the graph should not be nil. As such, any nil pointer
// or interface will result in an invalid graph.
//
// There is a reasonable use case for nil values, when part of the graph is used
// in different executables, e.g., a web server and CLI for the same application
// may reuse parts of the graph. This scenario has not been explored.
//
// Please submit an issue if you have this use case (the solution would be to
// identify an interface where an object in the graph can tell if it's valid or
// not, and non-vil values is reduced to the _default_ behaviour)
func (g *Graph[T]) Validate() error {
	return errors.Join(g.validate(reflect.ValueOf(g.instance))...)
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
	for f, fv := range g.inScopeFields(v) {
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

// inScopeFields return an iterator to visit all struct fields that are
// applicable for processing by Surgeon. These fields must be
//   - Exported
//   - Have a type that is in scope, if scopes are supplied.
func (g *Graph[T]) inScopeFields(v reflect.Value) iter.Seq2[reflect.StructField, reflect.Value] {
	t := v.Type()
	return func(yield func(reflect.StructField, reflect.Value) bool) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if !g.inScope(f.Type) {
				continue
			}
			if !yield(f, v.Field(i)) {
				return
			}
		}
	}
}
