package surgeon

import (
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
)

type graphDependency struct {
	m map[reflect.Type][]reflect.StructField
}

func newGraphDependency() graphDependency {
	return graphDependency{make(map[reflect.Type][]reflect.StructField)}
}

func (d graphDependency) clone() graphDependency {
	res := newGraphDependency()
	for k, v := range d.All() {
		res.set(k, slices.Clone(v))
	}
	return res
}

func (d graphDependency) append(k reflect.Type, v reflect.StructField) {
	d.m[k] = append(d.m[k], v)
}

func (d graphDependency) get(k reflect.Type) []reflect.StructField {
	res := d.m[k]
	return res
}
func (d graphDependency) delete(k reflect.Type) { delete(d.m, k) }

func (d graphDependency) set(k reflect.Type, v []reflect.StructField) {
	d.m[k] = v
}

func (d *graphDependency) AllSorted() iter.Seq2[reflect.Type, []reflect.StructField] {
	res := iterateTypesSorted(d.m)
	return func(yield func(k reflect.Type, v []reflect.StructField) bool) {
		for _, kv := range res {
			if !yield(kv.k, kv.v) {
				return
			}
		}
	}
}

func (d *graphDependency) All() iter.Seq2[reflect.Type, []reflect.StructField] {
	return func(yield func(k reflect.Type, v []reflect.StructField) bool) {
		if d == nil {
			return
		}
		for k, v := range d.m {
			if !yield(k, v) {
				return
			}
		}
	}
}

type graphDependencies struct {
	deps map[reflect.Type]*graphDependency
}

func newGraphDependencies() graphDependencies {
	return graphDependencies{make(map[reflect.Type]*graphDependency)}
}

func (d graphDependencies) All() iter.Seq2[reflect.Type, graphDependency] {
	return func(yield func(k reflect.Type, v graphDependency) bool) {
		for k, v := range d.deps {
			if !yield(k, *v) {
				return
			}
		}
	}
}

func (d graphDependencies) set(k reflect.Type, v graphDependency) {
	d.deps[k] = &v
}

func (d graphDependencies) clone() graphDependencies {
	res := newGraphDependencies()
	for k, v := range d.All() {
		res.set(k, v.clone())
	}
	return res
}

func (d graphDependencies) merge(src graphDependencies) {
	maps.Copy(d.deps, src.deps)
}
func (d graphDependencies) get(key reflect.Type) *graphDependency {
	return d.deps[key]
}

func (d graphDependencies) delete(k reflect.Type) { delete(d.deps, k) }

func (d graphDependencies) AllSorted() iter.Seq2[reflect.Type, *graphDependency] {
	res := iterateTypesSorted(d.deps)
	return func(yield func(k reflect.Type, v *graphDependency) bool) {
		for _, kv := range res {
			if !yield(kv.k, kv.v) {
				return
			}
		}
	}
}

/* -------- sort helper functions -------- */

func iterateTypesSorted[T any](types map[reflect.Type]T) []kv[reflect.Type, T] {
	keys := make([]reflect.Type, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	slices.SortFunc(
		keys,
		func(x, y reflect.Type) int { return strings.Compare(printType(x), printType(y)) },
	)
	res := make([]kv[reflect.Type, T], len(keys))
	for i, k := range keys {
		res[i] = kv[reflect.Type, T]{k, types[k]}
	}
	return res
}
