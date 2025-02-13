package surgeon

import (
	"reflect"
	"slices"
	"strings"
)

// Analyses a configured object. The resulting [GraphAnalysis] can be used to
// replace dependencies.
func Analyse[T any](instance T) *GraphAnalysis[T] {
	result := &GraphAnalysis[T]{instance: instance}
	result.buildDependencies()
	return result
}

type graphDependee struct {
	// type_ reflect.Type
	field reflect.StructField
	value reflect.Value
}

type graphDependency struct{}

type types []reflect.Type

func (t *types) append(types ...reflect.Type) {
	for _, type_ := range types {
		if !slices.Contains(*t, type_) {
			*t = append(*t, type_)
		}
	}
}

// The GraphAnalysis is the result of analysing a real object graph.
type GraphAnalysis[T any] struct {
	instance  T
	dependees map[reflect.Type][]graphDependee
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

func (a *GraphAnalysis[T]) buildDependencies() {
	a.dependees = make(map[reflect.Type][]graphDependee)
	a.dependencies = make(map[reflect.Type]map[reflect.Type][]reflect.StructField)
	a.buildTypeDependencies(reflect.ValueOf(a.instance), nil)
}

func (a *GraphAnalysis[T]) buildTypeDependencies(
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

			a.addDependee(type_, graphDependee{
				field: f,
				value: v,
			})
		}
		a.dependencies[type_] = typeDependencies
		return dependencies
	}
	return []reflect.Type{type_} // Or nil?
}

func (a *GraphAnalysis[T]) addDependee(t reflect.Type, d graphDependee) {
	a.dependees[t] = append(a.dependees[t], d)
}

func (a *GraphAnalysis[T]) addDependency(t reflect.Type, d graphDependee) {
	a.dependees[t] = append(a.dependees[t], d)
}

func (a *GraphAnalysis[T]) Create() T {
	return a.instance
}

// replace rebuilds the parts of the graph in graphObj by replacing dependencies
// of type t with the value newValue
func (a *GraphAnalysis[T]) replace(
	graphObj, newValue reflect.Value,
	type_ reflect.Type,
) reflect.Value {
	objType := graphObj.Type()

	isPointer := objType.Kind() == reflect.Pointer
	if isPointer {
		objType = objType.Elem()
		graphObj = graphObj.Elem()
	}

	deps := a.dependencies[objType]
	fieldsToUpdate := deps[type_]
	if len(fieldsToUpdate) == 0 {
		return graphObj
	}

	objCopyPtr := reflect.New(objType)
	objCopy := reflect.Indirect(objCopyPtr)
	objCopy.Set(graphObj)

	for _, f := range fieldsToUpdate {
		fieldValue := objCopy.FieldByIndex(f.Index)
		if f.Type == type_ {
			fieldValue.Set(newValue)
		} else {
			a.replace(fieldValue, newValue, type_)
		}
	}
	if isPointer {
		return objCopyPtr
	} else {
		return objCopy
	}
}

func Replace[T any, V any](a *GraphAnalysis[V], instance T) *GraphAnalysis[V] {
	clone := &GraphAnalysis[V]{
		a.instance,
		a.dependees,
		a.dependencies,
		a.interfaces,
	}

	replacedInstance := a.replace(
		reflect.ValueOf(a.instance), reflect.ValueOf(instance), reflect.TypeFor[T]()).
		Interface().(V)
	clone.instance = replacedInstance
	return clone
}
