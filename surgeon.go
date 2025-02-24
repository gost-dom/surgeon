package surgeon

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Scope interface{ InScope(reflect.Type) bool }

type PackagePrefixScope string

func (s PackagePrefixScope) InScope(t reflect.Type) bool {
	return strings.HasPrefix(t.PkgPath(), string(s))
}

// Analyses a configured object. The resulting [Graph] can be used to
// replace dependencies.
func BuildGraph[T any](instance T, scopes ...Scope) *Graph[T] {
	result := &Graph[T]{instance: instance, scopes: scopes}
	result.buildDependencies(false)
	return result
}

// Analyses and initialises the configured object. The resulting [Graph] can be used to
// replace dependencies.
//
// The graph is identical to the result of [BuildGraph], but this function
// implicitly initializes all components in the graph that implements the
// `Initer` interface
func InitGraph[T any](instance T, scopes ...Scope) *Graph[T] {
	result := &Graph[T]{instance: instance, scopes: scopes}
	result.buildDependencies(true)
	return result

}

type types []reflect.Type

func (t *types) append(types ...reflect.Type) {
	for _, type_ := range types {
		*t = append(*t, type_)
	}
}

func (t types) String() string {
	names := make([]string, len(t))
	for i, typ := range t {
		names[i] = typ.Name()
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
	dependencies map[reflect.Type]map[reflect.Type][]reflect.StructField
	initialized  map[reflect.Type]bool
	initializedV map[reflect.Value]bool
	interfaces   []reflect.Type
	ignoreNil    bool
	scopes       []Scope
}

func (a *Graph[T]) buildDependencies(init bool) {
	a.dependencies = make(map[reflect.Type]map[reflect.Type][]reflect.StructField)
	a.initialized = make(map[reflect.Type]bool)
	a.initializedV = make(map[reflect.Value]bool)
	v := reflect.ValueOf(a.instance)
	a.buildTypeDependencies(v, v, init, nil)
}

func (g *Graph[T]) IgnoreNil() *Graph[T] {
	clone := g.clone()
	clone.ignoreNil = true
	return clone
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

func isPointer(t reflect.Type) bool   { return t.Kind() == reflect.Pointer }
func isInterface(t reflect.Type) bool { return t.Kind() == reflect.Interface }

// Builds the dependency graph, and potentially initializes objects.
// - v is object being iterated for the dependency graph
// - initValue is the value that should be initialized (if enabled).
// - init indicates of values should be initialized
// - visitedTypes if for troubleshooting the code only.
func (a *Graph[T]) buildTypeDependencies(
	v reflect.Value,
	initValue reflect.Value,
	init bool,
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
		panic("Cyclic dependencies in graph: " + strings.Join(names, ", "))
	}
	visitedTypes = append(visitedTypes, type_)

	switch type_.Kind() {
	case reflect.Interface:
		if v.IsZero() && !a.ignoreNil {
			panic(fmt.Sprintf("surgeon: Value for %s (%s) is nil", type_.Name(), type_.PkgPath()))
		}
		if !slices.Contains(a.interfaces, type_) {
			a.interfaces = append(a.interfaces, type_)
		}
		fallthrough
	case reflect.Pointer:
		if v.IsZero() && !a.ignoreNil {
			innerType := type_.Elem()
			panic(
				fmt.Sprintf(
					"surgeon: Value for *%s (%s) is nil",
					innerType.Name(),
					innerType.PkgPath(),
				),
			)
		}
		e := v.Elem()
		newInitValue := e
		if v != initValue {
			newInitValue = initValue
		}
		tmp := a.buildTypeDependencies(e, newInitValue, init, visitedTypes)
		tmp.append(type_)
		if v == initValue {
			a.initValue(v, init)
		}
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
			fieldDependencies := a.buildTypeDependencies(fieldValue, initValue, init, visitedTypes)
			dependencies.append(fieldDependencies...)
			for _, depType := range fieldDependencies {
				typeDependencies[depType] = append(typeDependencies[depType], f)
			}
		}
		a.dependencies[type_] = typeDependencies
		dependencies.append(type_)
		a.initValue(initValue, init)
		return dependencies
	}
	return nil
}

func underlyingType(t reflect.Type) reflect.Type {
	for isInterface(t) || isPointer(t) {
		t = t.Elem()
	}
	return t
}

func (g *Graph[T]) initValue(v reflect.Value, init bool) {
	if init {
		t := v.Type()
		if isInterface(t) {
			// We will initialize the underlying type. Don't init twice
			return
		}
		p := isPointer(t)
		typeInitialized := g.initialized[v.Type()]
		valueInitialized := g.initializedV[v]
		if v.Type().Name() == "InitializableBNonPointer" ||
			(underlyingType(v.Type()).Name() == "Initializable") ||
			(isPointer(v.Type()) &&
				v.Type().Elem().Name() == "InitializableBNonPointer") {
		}
		typeInitialized = typeInitialized && p
		valueInitialized = valueInitialized && !p
		alreadyInitialized := typeInitialized || valueInitialized
		if i, ok := v.Interface().(Initer); ok && !alreadyInitialized {
			i.Init()
			g.initialized[v.Type()] = true
			g.initializedV[v] = true
		}
	}
}

func (a *Graph[T]) Instance() T {
	return a.instance
}

type debugInfo struct {
	Type reflect.Type
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
	stack []debugInfo,
) (reflect.Value, types) {
	objType := graphObj.Type()
	stack = append(stack, debugInfo{Type: objType})

	isInterface := objType.Kind() == reflect.Interface
	if isInterface {
		graphObj = graphObj.Elem()
		objType = graphObj.Type()
	}
	isPointer := objType.Kind() == reflect.Pointer
	if isPointer {
		graphObj = graphObj.Elem()
		objType = graphObj.Type()
	}

	deps := a.dependencies[objType]
	fieldsToUpdate := deps[type_]
	if len(fieldsToUpdate) == 0 {
		if isRoot {
			msg := fmt.Sprintf(
				"surgeon: Cannot replace type %s. No dependency in the graph",
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
			depsRemovedInIteration = a.getDependencyTypes(fieldValue.Elem().Type())
			fieldValue.Set(newValue)
		} else {
			var v reflect.Value
			v, depsRemovedInIteration = a.replace(fieldValue, newValue, type_, false, stack)
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
		depsRemoved = append(depsRemoved, depsRemovedInIteration...)
	}

	var result reflect.Value
	if isPointer {
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

func Debug[T any](a *Graph[T]) string {
	var b strings.Builder
	b.WriteString("Registered types:\n")
	for k, v := range a.dependencies {
		fieldToDeps := make(map[string][]reflect.Type)
		b.WriteString(fmt.Sprintf(" - %s\n", k.Name()))
		b.WriteString("    By dependency\n")
		for k2, f := range v {
			b.WriteString(fmt.Sprintf("     - %s (count: %d)\n", printType(k2), len(f)))
			for _, d := range f {
				n := d.Name
				fieldToDeps[n] = append(fieldToDeps[n], k2)
			}
		}
		b.WriteString("    By field\n")
		for n, deps := range fieldToDeps {
			b.WriteString(fmt.Sprintf("    - %s\n", n))
			for _, d := range deps {
				b.WriteString(fmt.Sprintf("      - %s\n", printType(d)))
			}
		}
	}
	return b.String()
}

func (g *Graph[T]) clone() *Graph[T] {
	return &Graph[T]{
		g.instance,
		g.dependencies,
		g.initialized,
		g.initializedV,
		g.interfaces,
		g.ignoreNil,
		g.scopes,
	}
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

	replacedInstance, _ := a.replace(
		reflect.ValueOf(a.instance), reflect.ValueOf(instance), t, true, nil)
	res.instance = replacedInstance.Interface().(T)
	return res
}

func ReplaceAll[T any](a *Graph[T], instance any) (res *Graph[T]) {
	t := reflect.TypeOf(instance)
	var interfaces []reflect.Type
	for _, i := range a.interfaces {
		if t.AssignableTo(i) {
			interfaces = append(interfaces, i)
		}
	}
	if len(interfaces) == 0 {
		panic(
			fmt.Sprintf(
				"surgeon.ReplaceAll: Instance of type %s does not implement an interface in the dependency graph of %s (has this already been replaced in this graph?)",
				t.Name(),
				reflect.TypeFor[T]().Name(),
			),
		)
	}
	res = a.clone()
	for _, i := range interfaces {
		v, _ := res.replace(reflect.ValueOf(res.instance), reflect.ValueOf(instance), i, true, nil)
		res.instance = v.Interface().(T)
	}
	return res
}
