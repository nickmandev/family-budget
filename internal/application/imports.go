package application

import (
	"context"
	"errors"

	"family-budget/internal/domain"
	"family-budget/internal/imports"
	"family-budget/internal/ports"
)

// StatementParser parses one uploaded statement without persisting it.
type StatementParser interface {
	Parse(filename string, data []byte) (imports.Statement, error)
}

// ImportService authorizes and previews uploaded bank statements.
type ImportService struct {
	uow    ports.UnitOfWork
	parser StatementParser
}

// NewImportService validates dependencies and returns the import service.
func NewImportService(uow ports.UnitOfWork, parser StatementParser) (*ImportService, error) {
	if uow == nil {
		return nil, errors.New("application: UnitOfWork is required")
	}
	if parser == nil {
		return nil, errors.New("application: StatementParser is required")
	}
	return &ImportService{uow: uow, parser: parser}, nil
}

// Preview verifies household membership, then parses an in-memory statement.
func (s *ImportService) Preview(ctx context.Context, actor domain.UserId, household domain.HouseholdId, filename string, data []byte) (imports.Statement, error) {
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		_, err := authorizeMember(ctx, repos, household, actor)
		return err
	})
	if err != nil {
		return imports.Statement{}, err
	}
	return s.parser.Parse(filename, data)
}
