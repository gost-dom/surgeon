package surgeon

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
