package app

import (
	"github.com/vance1852/cutVideo/internal/domain"
)

// domainPage returns the small page used when probing whether an installation is
// already bootstrapped.
func domainPage() domain.Page {
	return domain.Page{Number: 1, Size: 1, Sort: domain.SortAscending}
}

// principalZero is the anonymous principal used during first run bootstrap.
func principalZero() domain.Principal { return domain.Principal{} }

// principalSupervisor is the internal principal used for bootstrap provisioning
// of render seats. It never leaves the process and is always audited as such.
func principalSupervisor() domain.Principal {
	return domain.Principal{UserID: "bootstrap", Role: domain.RoleSupervisor}
}

// roleSupervisor exposes the supervisor role to the bootstrap path.
func roleSupervisor() domain.Role { return domain.RoleSupervisor }
