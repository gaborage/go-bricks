package tracking

import "context"

// repositoryMethodKey is the unexported context-key type under which the
// caller-supplied repository method name is stored. A dedicated struct type
// avoids collisions with context keys defined by other packages.
type repositoryMethodKey struct{}

// WithRepositoryMethod returns a copy of ctx that records the name of the
// repository method responsible for the database operations executed with it.
// The tracking layer reads this value and adds it as the `repository.method`
// attribute on the db.client.operation.duration metric, so dashboards can
// attribute query latency to the business operation that issued it (for example
// "GetCustomer" vs "InsertTransaction") rather than only the SQL verb.
//
// The value MUST be a static, low-cardinality identifier such as a method or
// function name. Because it becomes a metric attribute, interpolating
// per-request data (IDs, emails, ...) would explode metric cardinality. An empty
// method, or a nil ctx, is ignored and ctx is returned unchanged (the nil guard
// mirrors RepositoryMethodFromContext and avoids context.WithValue's
// "cannot create context from nil parent" panic).
func WithRepositoryMethod(ctx context.Context, method string) context.Context {
	if ctx == nil || method == "" {
		return ctx
	}
	return context.WithValue(ctx, repositoryMethodKey{}, method)
}

// RepositoryMethodFromContext returns the repository method name previously
// stored in ctx by WithRepositoryMethod. The boolean is false when no method is
// set (including for a nil context).
func RepositoryMethodFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	method, ok := ctx.Value(repositoryMethodKey{}).(string)
	return method, ok
}
