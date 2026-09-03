package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnsureCustomerLoginTransactionsSchema creates the durable table backing an
// in-flight Accounts sign-in.
//
// The transaction was process memory until now, which meant every restart
// dropped the sign-ins that were mid-flight: the browser came back from
// Accounts holding a state the new process had never issued and was told its
// state was invalid, with no way to tell that from a real forgery. Release
// Control redeploys Arena on its own, so that window is not rare.
//
// Only a SHA-256 digest of the state is stored, never the state itself, for
// the same reason customer_sessions stores only a digest of the session token:
// a leaked database row cannot be replayed as a callback. The browser-binding
// cookie is likewise held only as a digest, so the row is useless without the
// cookie the browser holds.
func EnsureCustomerLoginTransactionsSchema(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS customer_login_transactions (
			state_hash BYTEA PRIMARY KEY,
			browser_binding_digest BYTEA NOT NULL,
			nonce TEXT NOT NULL,
			pkce_verifier TEXT NOT NULL,
			return_to TEXT NOT NULL,
			popup BOOLEAN NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_customer_login_transactions_expires
			ON customer_login_transactions (expires_at)`,
	}
	for _, stmt := range statements {
		if _, err := Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("EnsureCustomerLoginTransactionsSchema exec: %w", err)
		}
	}
	return nil
}

// CustomerLoginTransaction is one in-flight sign-in, as stored.
type CustomerLoginTransaction struct {
	BrowserBindingDigest []byte
	Nonce                string
	PKCEVerifier         string
	ReturnTo             string
	Popup                bool
	ExpiresAt            time.Time
}

// InsertCustomerLoginTransaction records a sign-in that has just been sent to
// Accounts. stateHash and bindingDigest are SHA-256 digests, never the values.
func InsertCustomerLoginTransaction(
	ctx context.Context, stateHash, bindingDigest []byte,
	nonce, pkceVerifier, returnTo string, popup bool, expiresAt time.Time,
) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`INSERT INTO customer_login_transactions
		   (state_hash, browser_binding_digest, nonce, pkce_verifier, return_to, popup, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		stateHash, bindingDigest, nonce, pkceVerifier, returnTo, popup, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("InsertCustomerLoginTransaction: %w", err)
	}
	return nil
}

// ConsumeCustomerLoginTransaction claims a transaction exactly once.
//
// DELETE ... RETURNING is atomic, so two callbacks racing on one state cannot
// both be served: exactly one deletes the row and the other finds nothing. The
// expiry is part of the same statement rather than a read-then-check, so a row
// cannot be claimed by a caller that read it a moment before it lapsed.
//
// A missing or lapsed row is (false, nil), not an error — it is the ordinary
// answer for a stale, replayed or forged callback, and the caller distinguishes
// it from a database that is genuinely unwell.
func ConsumeCustomerLoginTransaction(ctx context.Context, stateHash []byte) (CustomerLoginTransaction, bool, error) {
	var txn CustomerLoginTransaction
	if Pool == nil {
		return txn, false, ErrNoDatabase
	}
	err := Pool.QueryRow(ctx,
		`DELETE FROM customer_login_transactions
		  WHERE state_hash = $1 AND expires_at > NOW()
		 RETURNING browser_binding_digest, nonce, pkce_verifier, return_to, popup, expires_at`,
		stateHash,
	).Scan(&txn.BrowserBindingDigest, &txn.Nonce, &txn.PKCEVerifier, &txn.ReturnTo, &txn.Popup, &txn.ExpiresAt)
	switch {
	case err == nil:
		return txn, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return txn, false, nil
	default:
		return txn, false, fmt.Errorf("ConsumeCustomerLoginTransaction: %w", err)
	}
}

// DeleteExpiredCustomerLoginTransactions purges lapsed rows so an abandoned
// sign-in cannot accumulate. Called from the handler's periodic cleanup loop.
func DeleteExpiredCustomerLoginTransactions(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	if _, err := Pool.Exec(ctx, `DELETE FROM customer_login_transactions WHERE expires_at < NOW()`); err != nil {
		return fmt.Errorf("DeleteExpiredCustomerLoginTransactions: %w", err)
	}
	return nil
}
