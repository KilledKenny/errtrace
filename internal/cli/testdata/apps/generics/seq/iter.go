package seq

import "iter"

// Parse lazily parses src, yielding each value or its parse error.
func /*@parse*/Parse[T any](src []string, parse func(string) (T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, s := range src {
			if !yield(parse(s)) {
				return
			}
		}
	}
}

// Collect drains it and stops at the first yielded error.
func /*@collect*/Collect[T any](it iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for v, err := range it {
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, nil
}
