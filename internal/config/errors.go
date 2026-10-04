package config

// PublicError marks a message that is safe to return to an authenticated user.
// Message must never contain credentials, raw command output, or secret contents.
// Err preserves the underlying cause for classification without exposing it.
type PublicError struct {
	Message string
	Err     error
}

func (e PublicError) Error() string {
	if e.Message == "" {
		return "operation failed"
	}
	return e.Message
}

func (e PublicError) PublicMessage() string { return e.Message }
func (e PublicError) Unwrap() error         { return e.Err }
