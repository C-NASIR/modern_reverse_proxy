package provider

import "fmt"

type ConflictError struct {
	ObjectType       string
	ObjectID         string
	Field            string
	ExistingProvider string
	IncomingProvider string
}

func (e *ConflictError) Error() string {
	if e == nil {
		return "provider conflict"
	}
	field := e.Field
	if field == "" {
		field = "unknown"
	}
	return fmt.Sprintf("conflict on %s %q field %s between %s and %s", e.ObjectType, e.ObjectID, field, e.ExistingProvider, e.IncomingProvider)
}
