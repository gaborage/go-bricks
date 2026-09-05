// Package deferred owns the query builders' deferred-error rule.
//
// ADR-031 settled how an identifier violation travels: a clause door records it
// and keeps returning the builder, and ToSQL() surfaces the FIRST one — a later
// violation never overwrites an earlier one, so the message names the argument
// the caller has to fix rather than whichever door ran last.
//
// The rule lived in four byte-identical methods over four plain fields, one per
// builder, and a fifth door (SubqueryColumn) had already started assigning the
// field directly. Owning it in one unexported field behind Fail/Err makes that
// impossible from any other package: there is nothing to assign.
package deferred

// Error carries a builder's deferred error. Its zero value is ready to use and
// reports no error, so a builder embedding it needs no constructor.
type Error struct {
	err error
}

// Fail records err as the builder's failure unless one is already recorded.
// A nil err is ignored, so a caller may hand over a funnel's result unchecked.
func (d *Error) Fail(err error) {
	if d.err == nil {
		d.err = err
	}
}

// Err returns the first recorded failure, or nil while the builder is intact.
func (d *Error) Err() error {
	return d.err
}
