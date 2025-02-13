package surgeon

import (
	"reflect"
)

// Analyses a configured object. The resulting [GraphAnalysis] can be used to
// replace dependencies.
func Analyse[T any](instance T) *GraphAnalysis[T] {
	return &GraphAnalysis[T]{instance}
}

// The GraphAnalysis is the result of analysing a real object graph.
type GraphAnalysis[T any] struct {
	instance T
}

func (a *GraphAnalysis[T]) Create() T {
	return a.instance
}

func Replace[T any, V any](a *GraphAnalysis[V], instance T) *GraphAnalysis[V] {
	instanceType := reflect.TypeFor[V]()
	isPointer := instanceType.Kind() == reflect.Pointer
	if isPointer {
		instanceType = instanceType.Elem()
	}
	depType := reflect.TypeFor[T]()
	instanceVal := reflect.ValueOf(a.instance)
	if isPointer {
		instanceVal = reflect.Indirect(instanceVal)
	}

	instanceCopyPtr := reflect.New(instanceType)
	instanceCopy := reflect.Indirect(instanceCopyPtr)
	instanceCopy.Set(instanceVal)

	for _, field := range reflect.VisibleFields(instanceType) {
		if field.Type == depType {
			instanceCopy.FieldByIndex(field.Index).Set(reflect.ValueOf(instance))
		}
	}
	if isPointer {
		instanceCopy = instanceCopy.Addr()
	}
	newInstance := instanceCopy.Interface().(V)
	return &GraphAnalysis[V]{instance: newInstance}
}
