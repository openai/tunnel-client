package oauth

type errorWithRedactedMessage struct {
	message string
	cause   error
}

func (e *errorWithRedactedMessage) Error() string {
	return e.message
}

func (e *errorWithRedactedMessage) Unwrap() error {
	return e.cause
}

func wrapErrorWithRedactedMessage(message string, cause error) error {
	return &errorWithRedactedMessage{message: message, cause: cause}
}
