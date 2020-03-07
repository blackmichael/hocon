package hocon

import "fmt"

type ParseError struct {
	errType string
	message string
	line    int
	column  int
}

func (p *ParseError) Error() string {
	return fmt.Sprintf("%s at: %d:%d, %s", p.errType, p.line, p.column, p.message)
}

func parseError(errType, message string, line, column int) *ParseError {
	return &ParseError{errType: errType, message: message, line: line, column: column}
}

func leadingPeriodError(line, column int) *ParseError {
	return parseError("leading period '.'", `(use quoted "" empty string if you want an empty element)`, line, column)
}

func trailingPeriodError(line, column int) *ParseError {
	return parseError("trailing period '.'", `(use quoted "" empty string if you want an empty element)`, line, column)
}

func adjacentPeriodsError(line, column int) *ParseError {
	return parseError("two adjacent periods '.'", `(use quoted "" empty string if you want an empty element)`, line, column)
}

func invalidSubstitutionError(message string, line, column int) *ParseError {
	return parseError("invalid substitution!", message, line, column)
}

func invalidConfigArrayError(message string, line, column int) *ParseError {
	return parseError("invalid config array!", message, line, column)
}

func invalidConfigObjectError(message string, line, column int) *ParseError {
	return parseError("invalid config object!", message, line, column)
}

func invalidKeyError(key string, line, column int) *ParseError {
	return parseError("invalid key!", fmt.Sprintf("%q is a forbidden character in keys", key), line, column)
}

func invalidValueError(message string, line, column int) *ParseError {
	return parseError("invalid value!", message, line, column)
}