package application

import (
	"context"

	"family-budget/internal/domain"
	"family-budget/internal/ports"
)

// MaxJournalPageSize is the largest page an HTTP caller may request.
const MaxJournalPageSize = 200

// JournalPage is one cursor page of committed operations.
type JournalPage struct {
	Items      []domain.JournalOperation
	NextCursor *ports.JournalCursor
}

// ListTransactions lists committed operations newest-last with (committed_at,
// id) ordering. A non-nil cursor is exclusive.
func (s *Service) ListTransactions(ctx context.Context, actor domain.UserId, household domain.HouseholdId, filter ports.JournalFilter) (JournalPage, error) {
	var page JournalPage
	err := s.uow.WithinTransaction(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if _, err := authorizeMember(ctx, repos, household, actor); err != nil {
			return err
		}
		limit := filter.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > MaxJournalPageSize {
			limit = MaxJournalPageSize
		}
		filter.Limit = limit + 1
		ops, err := repos.Journal.List(ctx, household, filter)
		if err != nil {
			return err
		}
		if len(ops) > limit {
			ops = ops[:limit]
			last := ops[len(ops)-1]
			page.NextCursor = &ports.JournalCursor{
				CommittedAt: last.CommittedAt().Time(),
				ID:          last.ID(),
			}
		}
		page.Items = ops
		return nil
	})
	return page, err
}
