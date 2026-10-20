// Package problem is the shared error type: what went wrong, in words for the user, and what
// kind of problem it is, so each HTTP layer can pick a status code in one place.
package problem

import (
	"errors"
	"net/http"
)

type Kind int

const (
	Failed      Kind = iota // a command or service refused (Taskwarrior, MyFitnessPal...)
	Invalid                 // the request was wrong: a bad id, an empty name
	NotFound                // the thing asked about doesn't exist
	Conflict                // it changed meanwhile, or something else is still running
	Unavailable             // a service didn't answer (Anki closed...)
)

type Error struct {
	Kind    Kind
	Message string
}

func (e Error) Error() string { return e.Message }

func Invalidf(message string) error     { return Error{Kind: Invalid, Message: message} }
func NotFoundf(message string) error    { return Error{Kind: NotFound, Message: message} }
func Conflictf(message string) error    { return Error{Kind: Conflict, Message: message} }
func Failedf(message string) error      { return Error{Kind: Failed, Message: message} }
func Unavailablef(message string) error { return Error{Kind: Unavailable, Message: message} }

// KindOf is err's kind; errors that aren't a problem.Error count as Failed.
func KindOf(err error) Kind {
	var p Error
	if errors.As(err, &p) {
		return p.Kind
	}
	return Failed
}

// Status is the HTTP status for err.
func Status(err error) int {
	switch KindOf(err) {
	case Invalid:
		return http.StatusBadRequest
	case NotFound:
		return http.StatusNotFound
	case Conflict:
		return http.StatusConflict
	case Unavailable:
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}
