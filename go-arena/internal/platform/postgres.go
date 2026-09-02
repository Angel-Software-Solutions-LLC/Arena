package platform

import (
	"context"
	"time"

	"arena-server/internal/db"
)

// PostgresAuthority is the same-database W1b strangler. It is the only shared
// authority adapter; it forwards to the existing, transactionally proven Arena
// records and does not create a second writable copy.
type PostgresAuthority struct{}

var _ Authority = PostgresAuthority{}

func NewPostgresAuthority() PostgresAuthority { return PostgresAuthority{} }

func (PostgresAuthority) UpsertVerifiedIdentity(ctx context.Context, email, issuer, subject, displayName string) (*db.CustomerAccount, error) {
	return db.UpsertVerifiedCustomerAccount(ctx, email, issuer, subject, displayName)
}

func (PostgresAuthority) SetSubscription(ctx context.Context, accountID string, active bool, syncedAt time.Time) (*db.SubscriptionSyncChange, error) {
	return db.SetCustomerSubscription(ctx, accountID, active, syncedAt)
}

func (PostgresAuthority) AccountCapacity(ctx context.Context, accountID string) (*db.PlatformAccountCapacity, error) {
	return db.GetPlatformAccountCapacity(ctx, accountID)
}

func (PostgresAuthority) TransitionProfile(ctx context.Context, command db.PlatformProfileTransition) (*db.PlatformProfileTransitionResult, error) {
	return db.TransitionPlatformProfile(ctx, command)
}

func (PostgresAuthority) Changes(ctx context.Context, afterChangeID int64, limit int) ([]db.PlatformChange, int64, error) {
	return db.ListPlatformChanges(ctx, afterChangeID, limit)
}

func (PostgresAuthority) AgentLinkHistory(ctx context.Context, accountID string, afterEventID int64, limit int) ([]db.PlatformAgentLinkEvent, int64, error) {
	return db.ListPlatformAgentLinkEvents(ctx, accountID, afterEventID, limit)
}

func (PostgresAuthority) LinkAgent(ctx context.Context, command db.PlatformAgentLinkCommand) (*db.PlatformAgentLinkResult, error) {
	return db.LinkPlatformAgent(ctx, command)
}

func (PostgresAuthority) UnlinkAgentExact(ctx context.Context, command db.PlatformAgentUnlinkCommand) (*db.PlatformAgentLinkResult, error) {
	return db.UnlinkPlatformAgent(ctx, command)
}

func (PostgresAuthority) PublicCatalog(ctx context.Context) (*db.CosmeticCatalog, error) {
	if db.Pool == nil {
		catalog := db.DefaultCosmeticCatalogData()
		return &catalog, nil
	}
	return db.GetPublicCosmeticCatalog(ctx)
}

func (PostgresAuthority) AdminCatalog(ctx context.Context) (*db.CosmeticCatalog, error) {
	return db.GetAdminCosmeticCatalog(ctx)
}

func (PostgresAuthority) UpsertCategory(ctx context.Context, category db.CosmeticCategory, actor string) (*db.CosmeticCategory, error) {
	return db.UpsertCosmeticCategory(ctx, category, actor)
}

func (PostgresAuthority) DeleteCategory(ctx context.Context, categoryID, actor string) (bool, error) {
	return db.DeleteCosmeticCategory(ctx, categoryID, actor)
}

func (PostgresAuthority) UpsertItem(ctx context.Context, item db.CosmeticItem, actor string) (*db.CosmeticItem, error) {
	return db.UpsertCosmeticCatalogItem(ctx, item, actor)
}

func (PostgresAuthority) DeleteItem(ctx context.Context, itemID, actor string) (bool, error) {
	return db.DeleteCosmeticCatalogItem(ctx, itemID, actor)
}

func (PostgresAuthority) UpsertPack(ctx context.Context, pack db.CosmeticPack, actor string) (*db.CosmeticPack, error) {
	return db.UpsertCosmeticPack(ctx, pack, actor)
}

func (PostgresAuthority) DeletePack(ctx context.Context, packID, actor string) (bool, error) {
	return db.DeleteCosmeticPack(ctx, packID, actor)
}

func (PostgresAuthority) ListAudit(ctx context.Context, limit int) ([]db.CosmeticCatalogAudit, error) {
	return db.ListCosmeticCatalogAudit(ctx, limit)
}

func (PostgresAuthority) AccountInventory(ctx context.Context, accountID string) (*db.CustomerCosmeticsInventory, error) {
	return db.GetCustomerCosmeticsInventory(ctx, accountID)
}

func (PostgresAuthority) ClaimArenaAgent(ctx context.Context, accountID, controlProof string) (*db.AccountBot, error) {
	return db.ClaimArenaAgentWithControlProof(ctx, accountID, controlProof)
}

func (PostgresAuthority) UnlinkAgent(ctx context.Context, accountID, agentID string) (bool, error) {
	return db.UnlinkBotFromCustomerAccount(ctx, accountID, agentID)
}
