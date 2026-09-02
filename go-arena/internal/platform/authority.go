// Package platform defines Arena's dependency on the shared identity and
// cosmetics authority. The first adapter remains in-process and uses Arena's
// existing PostgreSQL records; callers depend on this port so the records can
// move behind the versioned platform API without another ownership rewrite.
package platform

import (
	"context"
	"time"

	"arena-server/internal/db"
)

// IdentityAuthority owns verified customer identity binding and the one
// commerce fact attached to an identity: whether Accounts reported an active
// Arena subscription at sign-in. Customer sessions and credentials remain
// private to Arena.
type IdentityAuthority interface {
	UpsertVerifiedIdentity(context.Context, string, string, string, string) (*db.CustomerAccount, error)
	SetSubscription(context.Context, string, bool, time.Time) (*db.SubscriptionSyncChange, error)
}

// MetadataAuthority exposes the revisioned W1b.2 agent/profile contract. Its
// same-database implementation is ready for the later versioned platform HTTP
// adapter; W1b.2 deliberately does not expose a new public route.
type MetadataAuthority interface {
	AccountCapacity(context.Context, string) (*db.PlatformAccountCapacity, error)
	TransitionProfile(context.Context, db.PlatformProfileTransition) (*db.PlatformProfileTransitionResult, error)
	Changes(context.Context, int64, int) ([]db.PlatformChange, int64, error)
	AgentLinkHistory(context.Context, string, int64, int) ([]db.PlatformAgentLinkEvent, int64, error)
	LinkAgent(context.Context, db.PlatformAgentLinkCommand) (*db.PlatformAgentLinkResult, error)
	UnlinkAgentExact(context.Context, db.PlatformAgentUnlinkCommand) (*db.PlatformAgentLinkResult, error)
}

// CosmeticsAuthority owns the shared catalog and account-agent links. Bot
// loadout reads and equip writes are intentionally absent: those are Arena
// gameplay presentation state. There is no licence facet any more — the only
// entitlement is the Arena subscription, which Accounts owns and Arena caches
// on the account (see db.SetCustomerSubscription).
type CosmeticsAuthority interface {
	PublicCatalog(context.Context) (*db.CosmeticCatalog, error)
	AdminCatalog(context.Context) (*db.CosmeticCatalog, error)
	UpsertCategory(context.Context, db.CosmeticCategory, string) (*db.CosmeticCategory, error)
	DeleteCategory(context.Context, string, string) (bool, error)
	UpsertItem(context.Context, db.CosmeticItem, string) (*db.CosmeticItem, error)
	DeleteItem(context.Context, string, string) (bool, error)
	UpsertPack(context.Context, db.CosmeticPack, string) (*db.CosmeticPack, error)
	DeletePack(context.Context, string, string) (bool, error)
	ListAudit(context.Context, int) ([]db.CosmeticCatalogAudit, error)
	AccountInventory(context.Context, string) (*db.CustomerCosmeticsInventory, error)
	ClaimArenaAgent(context.Context, string, string) (*db.AccountBot, error)
	UnlinkAgent(context.Context, string, string) (bool, error)
}

// Authority is the one logical shared authority consumed by Arena. Consumer
// handlers accept the narrow facet they use so tests do not need broad mocks.
type Authority interface {
	IdentityAuthority
	MetadataAuthority
	CosmeticsAuthority
}
