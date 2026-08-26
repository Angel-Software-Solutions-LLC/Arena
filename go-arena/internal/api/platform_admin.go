package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"arena-server/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
)

/*
 * Angel Accounts decides who administers the platform. Arena reads that
 * decision; it does not make one of its own and it does not keep a copy.
 *
 * The contract (Accounts' OIDC product integration standard, "Platform
 * administrators"):
 *
 *   - The id_token and /userinfo carry `staff: true` and `staff_role` for
 *     exactly the identities that administer the platform.
 *   - Everybody else carries no staff claim at all. There is deliberately no
 *     `staff: false`, so the question Arena asks is whether the claim is
 *     PRESENT and true — never whether some value is truthy, and never
 *     anything about an address, an account name, or any other signal.
 *   - The claim is computed when the token is minted. It is a fact about that
 *     sign-in, not a property of the account, so it is never written down.
 *   - `staff_role` is an open vocabulary. An unrecognised value means "an
 *     administrator, nothing finer" — not "not an administrator", and not a
 *     reason to guess at a permission level Accounts did not describe.
 *
 * Administering the platform is orthogonal to owning things on it. Nothing
 * here touches an entitlement check; see customer_entitlements.go.
 */

const (
	platformAdminStaffClaim = "staff"
	platformAdminRoleClaim  = "staff_role"
)

// verifiedPlatformAdmin is what one validated id_token said about whether the
// person signing in administers the platform.
//
// Present is the whole answer. Role is carried for audit and for a future that
// distinguishes roles; today nothing branches on it, because the contract says
// an unrecognised role is still an administrator and Arena has no business
// inventing the finer grades.
type verifiedPlatformAdmin struct {
	Present bool
	Role    string
}

// platformAdminGrant is administrator authority as held by one signed-in
// session, and it exists only in memory.
//
// There is no column for this anywhere, and that is the design: the desk role
// in Accounts is the single source of truth, so a revocation has to take
// effect on its own. A durable flag would need somebody to remember to clear
// it, and the day nobody does is the day a former administrator still has the
// panel.
type platformAdminGrant struct {
	Role      string
	GrantedAt time.Time
	ExpiresAt time.Time
}

// platformAdminGrantTTL bounds how long one sign-in's answer is trusted for.
//
// A customer session lasts 30 days and slides forward every time it is used,
// which is right for "stay signed in to look at your bots" and wrong for
// "hold the admin panel". Arena cannot re-read the claim in the background —
// it deliberately keeps no access token and asks for no refresh token (see
// customer_entitlements.go) — so instead of stretching one answer across a
// month, the grant simply lapses and the next sign-in re-reads it.
//
// The window is the one Arena already uses for an administrator session,
// ARENA_OIDC_SESSION_TTL_HOURS. When the grant lapses the person stays signed
// in as a customer; only the admin authority goes.
func platformAdminGrantTTL() time.Duration {
	ttl := time.Duration(config.C.OIDCSessionTTL) * time.Hour
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return ttl
}

// newPlatformAdminGrant turns one sign-in's claim into session-scoped
// authority, never outliving the session that carries it.
func newPlatformAdminGrant(admin verifiedPlatformAdmin, now, sessionExpiry time.Time) *platformAdminGrant {
	if !admin.Present {
		return nil
	}
	expires := now.Add(platformAdminGrantTTL())
	if !sessionExpiry.IsZero() && sessionExpiry.Before(expires) {
		expires = sessionExpiry
	}
	return &platformAdminGrant{Role: admin.Role, GrantedAt: now, ExpiresAt: expires}
}

// platformAdminFromVerifiedClaims applies the presence rule to the claim set
// of an already-verified token.
//
// Anything that is not literally `true` — absent, null, false, a string, a
// number — is "not an administrator". Presence of the wrong shape is not
// evidence of anything, and this is the direction it has to fail in.
func platformAdminFromVerifiedClaims(claims map[string]json.RawMessage) verifiedPlatformAdmin {
	raw, present := claims[platformAdminStaffClaim]
	if !present {
		return verifiedPlatformAdmin{}
	}
	var staff bool
	if err := json.Unmarshal(raw, &staff); err != nil || !staff {
		return verifiedPlatformAdmin{}
	}
	role := ""
	if rawRole, ok := claims[platformAdminRoleClaim]; ok {
		// A role that is not a string is dropped rather than rejected: the
		// vocabulary is open and the administrator answer has already been
		// given by `staff`.
		var decoded string
		if err := json.Unmarshal(rawRole, &decoded); err == nil {
			role = strings.TrimSpace(decoded)
		}
	}
	return verifiedPlatformAdmin{Present: true, Role: role}
}

// platformAdminFromIDToken reads the claim from a token the verifier has
// already accepted. There is no other source: nothing here ever looks at a
// query parameter, a header, a cookie, or an unverified token body.
//
// A claim set that will not decode is treated as "not an administrator"
// rather than as a failed sign-in. The sign-in itself has already been
// validated against claims Arena does need; refusing it over an optional one
// would turn an Accounts-side change into an outage for every customer.
func platformAdminFromIDToken(idToken *oidc.IDToken) verifiedPlatformAdmin {
	if idToken == nil {
		return verifiedPlatformAdmin{}
	}
	var claims map[string]json.RawMessage
	if err := idToken.Claims(&claims); err != nil {
		return verifiedPlatformAdmin{}
	}
	return platformAdminFromVerifiedClaims(claims)
}

// platformAdminAt reports the administrator role this session carries right
// now, if any.
func (s *CustomerSession) platformAdminAt(now time.Time) (string, bool) {
	if s == nil || s.platformAdmin == nil {
		return "", false
	}
	if !now.Before(s.platformAdmin.ExpiresAt) {
		return "", false
	}
	return s.platformAdmin.Role, true
}

/*
 * platformAdminPrincipal resolves an Angel Accounts sign-in into Arena admin
 * authority for one request.
 *
 * Three outcomes, all of them decided here and none of them written to the
 * response: an administrator (principal, authorized), somebody this cookie
 * says nothing about (falls through to the token paths, so nothing that works
 * today stops working), and an administrator whose *mutation* is refused.
 *
 * The refusal is returned as a reason rather than written, because the caller
 * still has token paths left to try and two answers must not both be sent. It
 * is the caller that decides a browser with no X-Admin-Token deserves to hear
 * this reason instead of a misleading "missing X-Admin-Token".
 *
 * Mutations are held to the same same-origin and CSRF checks that
 * MakeCustomerAuthMiddleware applies, because this is the customer cookie and
 * a browser attaches it to everything on its own. That is strictly stronger
 * than what the admin SSO cookie is held to, and deliberately so: the admin
 * cookie is only ever sent to the admin app, and this one is sent from every
 * ordinary page of the site.
 */
func (h *CustomerOIDCHandler) platformAdminPrincipal(r *http.Request) (principal string, authorized bool, denyReason string) {
	if h == nil {
		return "", false, ""
	}
	session := h.GetSession(r)
	if session == nil {
		return "", false, ""
	}
	role, ok := session.platformAdminAt(time.Now())
	if !ok {
		return "", false, ""
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if !customerMutationHasSameOrigin(r) {
			return "", false, "cross-origin admin mutation rejected"
		}
		provided := r.Header.Get("X-CSRF-Token")
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
			return "", false, "invalid CSRF token"
		}
	}
	// The account id, and not the address — the same rule the sign-in log
	// line follows. The role is appended when Accounts named one.
	principal = "accounts-staff:" + session.AccountID
	if role != "" {
		principal += ":" + role
	}
	return principal, true, ""
}
