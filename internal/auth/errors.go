// Package auth implements email/password authentication and revocable
// sessions (PLATFORM_DECISIONS §B).
//
// It is an application service: it depends on the persistence ports and the
// standard library, never on net/http. The transport layer maps the codes below
// with the same table it uses for domain errors. The only transport-adjacent
// detail here is that failures are deliberately generic: unknown email and
// wrong password both return ErrInvalidCredentials.
package auth

// Stable authentication error codes. They are part of the API contract and are
// translated to HTTP statuses by internal/transport/http/errors.go.
const (
	CodeInvalidCredentials     = "INVALID_CREDENTIALS"
	CodeUnauthenticated        = "UNAUTHENTICATED"
	CodeEmailAlreadyRegistered = "EMAIL_ALREADY_REGISTERED"
	CodeInvalidValue           = "INVALID_VALUE"
)

// Error is an application error carrying a stable code, a safe message and an
// optional request field. It never wraps a database error, so the transport
// layer cannot accidentally expose one.
type Error struct {
	Code    string
	Message string
	Field   string
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrorCode reports the stable machine-readable code.
func (e *Error) ErrorCode() string { return e.Code }

// ErrorField reports the offending request field, when there is one.
func (e *Error) ErrorField() string { return e.Field }

// ErrorDetails reports optional non-sensitive context.
func (e *Error) ErrorDetails() any { return e.Details }

var (
	// ErrInvalidCredentials is the single login failure. It is returned for an
	// unknown email, a wrong password and malformed credentials so the response
	// cannot be used for account enumeration.
	ErrInvalidCredentials = &Error{Code: CodeInvalidCredentials, Message: "Invalid credentials."}

	// ErrUnauthenticated means no valid session was presented.
	ErrUnauthenticated = &Error{Code: CodeUnauthenticated, Message: "Authentication is required."}

	// ErrEmailAlreadyRegistered is returned by registration. Registration is
	// not an authentication oracle in the same sense, but the message is still
	// generic and the field is the only extra information.
	ErrEmailAlreadyRegistered = &Error{
		Code:    CodeEmailAlreadyRegistered,
		Message: "An account with this email already exists.",
		Field:   "email",
	}

	ErrEmailInvalid = &Error{
		Code:    CodeInvalidValue,
		Message: "A valid email address is required.",
		Field:   "email",
	}
	ErrPasswordInvalid = &Error{
		Code:    CodeInvalidValue,
		Message: "Password must be between 8 and 1024 bytes.",
		Field:   "password",
	}
)
