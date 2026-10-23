// Package problem is the shared error type: what went wrong, in words for the user, and what
// kind of problem it is, so each HTTP layer can pick a status code in one place.
package problem

import (
	"errors"
	"log"
	"net/http"
)

type Kind int

const (
	Failed      Kind = iota // a command or service refused (Taskwarrior, MyFitnessPal...)
	Invalid                 // the request was wrong: a bad id, an empty name
	NotFound                // the thing asked about doesn't exist
	Conflict                // it changed meanwhile, or something else is still running
	Unavailable             // a service didn't answer (Anki closed...)
	Forbidden               // allowed for someone, but not for this user (admin-only, the agent policy)
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
func Forbiddenf(message string) error   { return Error{Kind: Forbidden, Message: message} }

// Hidden is what people see instead of an error that isn't a problem.Error.
const Hidden = "Something went wrong on the server. Its log has the details."

// Public is what may be shown to people about err: the message of the problem.Error inside it,
// or Hidden for anything else (a file the server couldn't open, a command's output), so paths
// and other internals never reach a page. Whatever it leaves out goes to the server's log.
func Public(err error) string {
	var p Error
	if errors.As(err, &p) {
		if err.Error() != p.Message {
			log.Print(err)
		}
		return p.Message
	}
	log.Printf("error: %v", err)
	return Hidden
}

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
	case Forbidden:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}
