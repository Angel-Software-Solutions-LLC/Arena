package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"arena-server/internal/db"

	"github.com/go-chi/chi/v5"
)

type adminCosmeticCategoryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
	SortOrder   int    `json:"sort_order"`
}

type adminCosmeticItemRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	CategoryID    string `json:"category_id"`
	Slot          string `json:"slot"`
	AssetKey      string `json:"asset_key"`
	Rarity        string `json:"rarity"`
	PriceCents    int    `json:"price_cents"`
	Currency      string `json:"currency"`
	IsFree        bool   `json:"is_free"`
	IsPurchasable bool   `json:"is_purchasable"`
	IsActive      bool   `json:"is_active"`
	SortOrder     int    `json:"sort_order"`
}

type adminCosmeticPackRequest struct {
	CategoryID    string   `json:"category_id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	PriceCents    int      `json:"price_cents"`
	Currency      string   `json:"currency"`
	IsFree        bool     `json:"is_free"`
	IsPurchasable bool     `json:"is_purchasable"`
	IsActive      bool     `json:"is_active"`
	SortOrder     int      `json:"sort_order"`
	ItemIDs       []string `json:"item_ids"`
}

func registerCosmeticsAdminRoutes(router chi.Router, handler *CosmeticsHandler) {
	router.Get("/cosmetics/catalog", handler.AdminCatalog)
	router.Get("/cosmetics/audit", handler.AdminAudit)
	router.Put("/cosmetics/categories/{category_id}", handler.UpsertAdminCategory)
	router.Delete("/cosmetics/categories/{category_id}", handler.DeleteAdminCategory)
	router.Put("/cosmetics/items/{item_id}", handler.UpsertAdminItem)
	router.Delete("/cosmetics/items/{item_id}", handler.DeleteAdminItem)
	router.Put("/cosmetics/packs/{pack_id}", handler.UpsertAdminPack)
	router.Delete("/cosmetics/packs/{pack_id}", handler.DeleteAdminPack)
	// Catalog administration only. Who may wear what is decided by the Arena
	// subscription held in Angel Accounts; there is no grant, licence,
	// membership or order route here to second-guess it.
}

func (h *CosmeticsHandler) AdminCatalog(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	catalog, err := h.authority.AdminCatalog(r.Context())
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to load cosmetic catalog")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"categories":   catalog.Categories,
		"packs":        catalog.Packs,
		"items":        catalog.Items,
		"subscription": catalogSubscription(),
	})
}

func (h *CosmeticsHandler) UpsertAdminCategory(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	var request adminCosmeticCategoryRequest
	if err := decodeStrictCosmeticAdminJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic category")
		return
	}
	category := db.CosmeticCategory{
		ID:          strings.TrimSpace(chi.URLParam(r, "category_id")),
		Name:        strings.TrimSpace(request.Name),
		Description: strings.TrimSpace(request.Description),
		IsActive:    request.IsActive,
		SortOrder:   request.SortOrder,
	}
	if err := db.ValidateCosmeticCategory(category); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic category")
		return
	}
	result, err := h.authority.UpsertCategory(r.Context(), category, cosmeticAdminActor(r))
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to save cosmetic category")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": result, "live_refreshed": h.refreshConnectedBotVisuals(r.Context()),
	})
}

func (h *CosmeticsHandler) DeleteAdminCategory(w http.ResponseWriter, r *http.Request) {
	h.deleteAdminCatalogEntity(w, r, "category", chi.URLParam(r, "category_id"))
}

func (h *CosmeticsHandler) UpsertAdminItem(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	var request adminCosmeticItemRequest
	if err := decodeStrictCosmeticAdminJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic item")
		return
	}
	item := db.CosmeticItem{
		ID:            strings.TrimSpace(chi.URLParam(r, "item_id")),
		Name:          strings.TrimSpace(request.Name),
		Description:   strings.TrimSpace(request.Description),
		CategoryID:    strings.ToLower(strings.TrimSpace(request.CategoryID)),
		Slot:          strings.ToLower(strings.TrimSpace(request.Slot)),
		AssetKey:      strings.ToLower(strings.TrimSpace(request.AssetKey)),
		Rarity:        strings.ToLower(strings.TrimSpace(request.Rarity)),
		PriceCents:    request.PriceCents,
		Currency:      strings.ToUpper(strings.TrimSpace(request.Currency)),
		IsFree:        request.IsFree,
		IsPurchasable: request.IsPurchasable,
		IsActive:      request.IsActive,
		SortOrder:     request.SortOrder,
	}
	if err := db.ValidateCosmeticItem(item); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic item")
		return
	}
	result, err := h.authority.UpsertItem(r.Context(), item, cosmeticAdminActor(r))
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to save cosmetic item")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"item": result, "gameplay": "unchanged", "live_refreshed": h.refreshConnectedBotVisuals(r.Context()),
	})
}

func (h *CosmeticsHandler) DeleteAdminItem(w http.ResponseWriter, r *http.Request) {
	h.deleteAdminCatalogEntity(w, r, "item", chi.URLParam(r, "item_id"))
}

func (h *CosmeticsHandler) UpsertAdminPack(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	var request adminCosmeticPackRequest
	if err := decodeStrictCosmeticAdminJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic pack")
		return
	}
	itemIDs := make([]string, len(request.ItemIDs))
	for index, itemID := range request.ItemIDs {
		itemIDs[index] = strings.ToLower(strings.TrimSpace(itemID))
	}
	pack := db.CosmeticPack{
		ID:            strings.TrimSpace(chi.URLParam(r, "pack_id")),
		CategoryID:    strings.ToLower(strings.TrimSpace(request.CategoryID)),
		Name:          strings.TrimSpace(request.Name),
		Description:   strings.TrimSpace(request.Description),
		PriceCents:    request.PriceCents,
		Currency:      strings.ToUpper(strings.TrimSpace(request.Currency)),
		IsFree:        request.IsFree,
		IsPurchasable: request.IsPurchasable,
		IsActive:      request.IsActive,
		SortOrder:     request.SortOrder,
		ItemIDs:       itemIDs,
	}
	if err := db.ValidateCosmeticPack(pack); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cosmetic pack")
		return
	}
	result, err := h.authority.UpsertPack(r.Context(), pack, cosmeticAdminActor(r))
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to save cosmetic pack")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"pack": result, "gameplay": "unchanged"})
}

func (h *CosmeticsHandler) DeleteAdminPack(w http.ResponseWriter, r *http.Request) {
	h.deleteAdminCatalogEntity(w, r, "pack", chi.URLParam(r, "pack_id"))
}

func (h *CosmeticsHandler) AdminAudit(w http.ResponseWriter, r *http.Request) {
	setAdminCatalogNoStore(w)
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = value
	}
	if limit > 200 {
		limit = 200
	}
	events, err := h.authority.ListAudit(r.Context(), limit)
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to load cosmetic catalog audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "count": len(events)})
}

func (h *CosmeticsHandler) deleteAdminCatalogEntity(w http.ResponseWriter, r *http.Request, entityType, rawID string) {
	setAdminCatalogNoStore(w)
	entityID := strings.TrimSpace(rawID)
	actor := cosmeticAdminActor(r)
	var (
		deleted bool
		err     error
	)
	switch entityType {
	case "category":
		deleted, err = h.authority.DeleteCategory(r.Context(), entityID, actor)
	case "item":
		deleted, err = h.authority.DeleteItem(r.Context(), entityID, actor)
	case "pack":
		deleted, err = h.authority.DeletePack(r.Context(), entityID, actor)
	default:
		err = db.ErrCosmeticCatalogInvalid
	}
	if err != nil {
		writeCosmeticCatalogError(w, err, "failed to delete cosmetic "+entityType)
		return
	}
	liveRefreshed := 0
	if deleted && (entityType == "category" || entityType == "item") {
		liveRefreshed = h.refreshConnectedBotVisuals(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted, "id": entityID, "live_refreshed": liveRefreshed,
	})
}

func decodeStrictCosmeticAdminJSON(r *http.Request, destination interface{}) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func cosmeticAdminActor(r *http.Request) string {
	if principal := adminPrincipalFromContext(r.Context()); principal != "" {
		return principal
	}
	if strings.TrimSpace(r.Header.Get("X-Admin-Token")) != "" {
		return "admin-token"
	}
	return "admin-session"
}

func writeCosmeticCatalogError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, db.ErrCosmeticCatalogBuiltin):
		writeError(w, http.StatusConflict, "built-in cosmetic entries cannot be deleted; deactivate them instead")
	case errors.Is(err, db.ErrCosmeticCatalogInvalid):
		writeError(w, http.StatusBadRequest, "invalid cosmetic catalog metadata")
	case errors.Is(err, db.ErrCosmeticCatalogConflict):
		writeError(w, http.StatusConflict, "cosmetic catalog change conflicts with existing data")
	case errors.Is(err, db.ErrCosmeticCategoryNotFound), errors.Is(err, db.ErrCosmeticNotFound), errors.Is(err, db.ErrCosmeticPackNotFound):
		writeError(w, http.StatusNotFound, "cosmetic catalog entry not found")
	case errors.Is(err, db.ErrNoDatabase):
		writeError(w, http.StatusServiceUnavailable, "database not available")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func setAdminCatalogNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
