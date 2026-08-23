package domain

import "strings"

// Pagination limits.
const (
	MinPageSize     = 1
	MaxPageSize     = 100
	DefaultPageSize = 20
)

// SortDirection enumerates the accepted sort orders.
type SortDirection string

// Sort directions.
const (
	SortAscending  SortDirection = "asc"
	SortDescending SortDirection = "desc"
)

// Page describes a requested slice of a collection.
type Page struct {
	Number int
	Size   int
	SortBy string
	Sort   SortDirection
}

// NewPage normalizes a requested page, rejecting values outside the contract.
func NewPage(number, size int, sortBy string, direction SortDirection) (Page, error) {
	if number < 0 {
		return Page{}, NewValidationError("page", "must not be negative")
	}
	if number == 0 {
		number = 1
	}
	if size == 0 {
		size = DefaultPageSize
	}
	if size < MinPageSize || size > MaxPageSize {
		return Page{}, NewValidationError("page_size", "must be between 1 and 100")
	}
	switch direction {
	case "":
		direction = SortDescending
	case SortAscending, SortDescending:
	default:
		return Page{}, NewValidationError("sort", "must be asc or desc")
	}
	return Page{Number: number, Size: size, SortBy: strings.TrimSpace(sortBy), Sort: direction}, nil
}

// Offset returns the SQL offset for the page.
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// Limit returns the SQL limit for the page.
func (p Page) Limit() int { return p.Size }

// Descending reports whether results must be ordered newest first.
func (p Page) Descending() bool { return p.Sort != SortAscending }

// PageResult carries one page of results plus the total count computed with the
// same filter as the item query.
type PageResult[T any] struct {
	Items      []T
	Total      int
	Page       int
	Size       int
	TotalPages int
}

// NewPageResult builds a page result and derives the page count.
func NewPageResult[T any](items []T, total int, page Page) PageResult[T] {
	pages := 0
	if page.Size > 0 {
		pages = total / page.Size
		if total%page.Size != 0 {
			pages++
		}
	}
	if items == nil {
		items = []T{}
	}
	return PageResult[T]{Items: items, Total: total, Page: page.Number, Size: page.Size, TotalPages: pages}
}
