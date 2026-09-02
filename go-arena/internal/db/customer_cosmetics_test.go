package db

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeCustomerEmail(t *testing.T) {
	normalized, err := NormalizeCustomerEmail("  Owner+Arena@Example.COM  ")
	if err != nil || normalized != "owner+arena@example.com" {
		t.Fatalf("NormalizeCustomerEmail = (%q, %v)", normalized, err)
	}
	for _, invalid := range []string{"", "owner", "Owner <owner@example.com>", "owner@example.com,other@example.com"} {
		if _, err := NormalizeCustomerEmail(invalid); !errors.Is(err, ErrCustomerEmailInvalid) {
			t.Errorf("NormalizeCustomerEmail(%q) error = %v, want ErrCustomerEmailInvalid", invalid, err)
		}
	}
}

func TestCustomerCosmeticsQueriesNilPoolReturnError(t *testing.T) {
	original := Pool
	Pool = nil
	t.Cleanup(func() { Pool = original })

	if _, err := GetCustomerAccount(t.Context(), "account"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("GetCustomerAccount error = %v, want ErrNoDatabase", err)
	}
	if _, err := SetCustomerSubscription(t.Context(), "account", true, time.Now()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("SetCustomerSubscription error = %v, want ErrNoDatabase", err)
	}
	if _, err := EquipCustomerCosmetic(t.Context(), "account", "bot", CosmeticSlotBotSkin, "skin"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("EquipCustomerCosmetic error = %v, want ErrNoDatabase", err)
	}
	if _, err := EquipCustomerCosmetic(t.Context(), "account", "bot", "hat", "skin"); !errors.Is(err, ErrInvalidCosmeticSlot) {
		t.Fatalf("EquipCustomerCosmetic bad slot error = %v, want ErrInvalidCosmeticSlot", err)
	}
	if _, err := ListAccountBotLoadouts(t.Context(), "account"); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("ListAccountBotLoadouts error = %v, want ErrNoDatabase", err)
	}
}
