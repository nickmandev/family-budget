package imports

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrUnknownFormat means no registered parser recognized the input.
	ErrUnknownFormat = errors.New("unknown statement format")
	// ErrInvalidStatement means a recognized statement could not be safely parsed.
	ErrInvalidStatement = errors.New("invalid statement")
)

// invalidStatement builds a fatal parse error that carries a safe,
// value-free reason alongside ErrInvalidStatement.
func invalidStatement(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidStatement, reason)
}

// StatementReason returns the safe reason a parser attached to an
// ErrInvalidStatement error, or "" when the error carries none. Reasons
// never contain statement contents.
func StatementReason(err error) string {
	if err == nil || !errors.Is(err, ErrInvalidStatement) {
		return ""
	}
	prefix := ErrInvalidStatement.Error() + ": "
	message := err.Error()
	if strings.HasPrefix(message, prefix) {
		return message[len(prefix):]
	}
	return ""
}

// Registry dispatches statements to bank-specific parsers.
type Registry struct {
	parsers []Parser
}

// NewRegistry creates a parser registry. Nil parsers are ignored.
func NewRegistry(parsers ...Parser) *Registry {
	registry := &Registry{parsers: make([]Parser, 0, len(parsers))}
	for _, parser := range parsers {
		if parser != nil {
			registry.parsers = append(registry.parsers, parser)
		}
	}
	return registry
}

// Parse detects a parser from content and filename, then parses the statement.
func (r *Registry) Parse(filename string, data []byte) (Statement, error) {
	for _, parser := range r.parsers {
		if parser.Detect(filename, data) {
			statement, err := parser.Parse(data)
			if err != nil {
				return Statement{}, err
			}
			statement.Bank = parser.Bank()
			statement.Format = parser.Format()
			if statement.Entries == nil {
				statement.Entries = []Entry{}
			}
			return statement, nil
		}
	}
	return Statement{}, ErrUnknownFormat
}

// Supported returns unique registered bank/format identifiers.
func (r *Registry) Supported() []string {
	seen := make(map[string]struct{}, len(r.parsers))
	result := make([]string, 0, len(r.parsers))
	for _, parser := range r.parsers {
		key := strings.ToLower(parser.Bank()) + ":" + strings.ToLower(parser.Format())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, parser.Bank()+":"+parser.Format())
	}
	return result
}
