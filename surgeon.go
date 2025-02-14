package surgeon

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Analyses a configured object. The resulting [Graph] can be used to
// replace dependencies.
func BuildGraph[T any](instance T) *Graph[T] {
	result := &Graph[T]{instance: instance}
	result.buildDependencies()
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
	interfaces   []reflect.Type
}

func (a *Graph[T]) buildDependencies() {
	a.dependencies = make(map[reflect.Type]map[reflect.Type][]reflect.StructField)
	a.buildTypeDependencies(reflect.ValueOf(a.instance), nil)
}

func (a *Graph[T]) buildTypeDependencies(
	v reflect.Value,
	visitedTypes []reflect.Type,
) types {
	type_ := v.Type()
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
		a.interfaces = append(a.interfaces, type_)
		fallthrough
	case reflect.Pointer:
		tmp := a.buildTypeDependencies(v.Elem(), visitedTypes)
		tmp.append(type_)
		return tmp
	case reflect.Struct:
		var dependencies types
		typeDependencies := make(map[reflect.Type][]reflect.StructField)
		for _, f := range reflect.VisibleFields(type_) {
			fieldValue := v.FieldByIndex(f.Index)
			fieldDependencies := a.buildTypeDependencies(fieldValue, visitedTypes)
			dependencies.append(fieldDependencies...)
			for _, depType := range fieldDependencies {
				typeDependencies[depType] = append(typeDependencies[depType], f)
			}
		}
		a.dependencies[type_] = typeDependencies
		dependencies.append(type_)
		return dependencies
	}
	return types{type_} // Or nil?
}

func (a *Graph[T]) Instance() T {
	return a.instance
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
) (reflect.Value, types) {
	objType := graphObj.Type()

	isPointer := objType.Kind() == reflect.Pointer
	isInterface := objType.Kind() == reflect.Interface
	if isPointer || isInterface {
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
			msg := fmt.Sprintf(
				"surgeon: Dependency tree is in an inconsistent state %s has no dependency to %s. Please submit an issue at %s",
				objType.Name(), type_.Name(),
				newIssueUrl,
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
			v, depsRemovedInIteration = a.replace(fieldValue, newValue, type_, false)
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
	if isPointer {
		return objCopyPtr, depsRemoved
	} else {
		return objCopy, depsRemoved
	}
}

func (a *Graph[T]) getDependencyTypes(t reflect.Type) types {
	var res types
	for k, v := range a.dependencies[t] {
		for range v {
			res = append(res, k)
		}
	}
	return res
}

func Debug[T any](a *Graph[T]) string {
	var b strings.Builder
	b.WriteString("Registered types:\n")
	for k, v := range a.dependencies {
		fieldToDeps := make(map[string][]reflect.Type)
		b.WriteString(fmt.Sprintf(" - %s\n", k.Name()))
		b.WriteString("    By dependency\n")
		for k2, f := range v {
			b.WriteString(fmt.Sprintf("     - %s (count: %d)\n", k2.Name(), len(f)))
			for _, d := range f {
				n := d.Name
				fieldToDeps[n] = append(fieldToDeps[n], k2)
			}
		}
		b.WriteString("    By field\n")
		for n, deps := range fieldToDeps {
			b.WriteString(fmt.Sprintf("    - %s\n", n))
			for _, d := range deps {
				b.WriteString(fmt.Sprintf("      - %s\n", d.Name()))
			}
		}
	}
	return b.String()
}

// Create a new graph with a dependency replaced by a new implementation. Panics
// if the root object in the graph doesn't include the replaced type in the
// dependency tree. Panics if the replaced type isn't an interface.
func Replace[V any, T any](a *Graph[T], instance V) *Graph[T] {
	clone := &Graph[T]{
		a.instance,
		a.dependencies,
		a.interfaces,
	}

	t := reflect.TypeFor[V]()
	if t.Kind() != reflect.Interface {
		panic("surgeon: Replaced type must be an interface")
	}

	replacedInstance, _ := a.replace(
		reflect.ValueOf(a.instance), reflect.ValueOf(instance), t, true)
	clone.instance = replacedInstance.Interface().(T)
	return clone
}
