package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
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
 * administrators"): exactly one claim admits.
 *
 *   - `product_admin: true` is a per-person, per-product grant: somebody the
 *     desk has deliberately made an administrator of Arena. Accounts computes
 *     it against the product the token was minted for, so on a token Arena
 *     verified it can only mean Arena. It is the whole of the answer.
 *   - `staff: true` (with `staff_role`) marks the support desk. It is
 *     identification, not authority: it says the person signing in works the
 *     desk, and Arena records it on the sign-in line so an operator can see
 *     who was here. It opens nothing. A desk owner who has not been granted
 *     Arena reaches exactly as much of the admin panel as any customer does,
 *     which is none of it.
 *   - Everybody else carries neither claim. There is deliberately no
 *     `product_admin: false` or `staff: false`, so the question Arena asks is
 *     whether the grant claim is PRESENT and true — never whether some value
 *     is truthy, and never anything about an address, an account name, or any
 *     other signal.
 *   - Both are computed when the token is minted. They are facts about that
 *     sign-in, not properties of the account, so they are never written down.
 *
 * The desk claim used to admit as well, and that is the bug this shape exists
 * to prevent: it made every owner and admin of the support desk an
 * administrator of every Angel product at once, with nothing provisioned and
 * nothing to revoke per product. Administering Arena is now something somebody
 * is given, one product at a time, from that person's record in the Accounts
 * console — and taking it away is revoking that one grant.
 *
 * `staff_role` is an open vocabulary and nothing branches on it: it is carried
 * for audit, beside `staff: true`, and an unrecognised value is recorded as it
 * arrived.
 *
 * Administering the platform is orthogonal to owning things on it. Nothing
 * here touches an entitlement check; see customer_entitlements.go.
 */

const (
	platformAdminStaffClaim   = "staff"
	platformAdminRoleClaim    = "staff_role"
	platformAdminProductClaim = "product_admin"
)

// The one way a sign-in can carry administrator authority, named the way the
// principal and the session-info endpoint report it. It stays a named value
// rather than a bare string because the contract says to expect further
// administrator claims one day, and this is where a second one would land.
const platformAdminAuthorityProduct = "product_admin"

// verifiedPlatformAdmin is what one validated id_token said about the person
// signing in: whether they administer Arena, and whether they work the desk.
//
// Present — the Arena grant, and the whole of the authority answer — is
// ProductAdmin and nothing else. Staff and Role are the desk claim, recorded
// so the sign-in line and the panel can say a desk identity was here; neither
// is ever consulted to decide a route. Keeping them apart is the point: the
// two questions "may this person administer Arena" and "does this person work
// for us" have different answers and used to share one.
type verifiedPlatformAdmin struct {
	Present      bool
	Staff        bool
	ProductAdmin bool
	Role         string
}

// authority names the claim that admitted this sign-in, or "" for a sign-in
// that was not admitted. Only the per-product grant admits, so a desk identity
// without one has no authority to name.
func (a verifiedPlatformAdmin) authority() string {
	if a.ProductAdmin {
		return platformAdminAuthorityProduct
	}
	return ""
}

// platformAdminGrant is administrator authority as held by one signed-in
// session, and it exists only in memory.
//
// There is no column for this anywhere, and that is the design: the grant in
// Accounts is the single source of truth, so a revocation has to take effect
// on its own. A durable flag would need somebody to remember to clear it, and
// the day nobody does is the day a former administrator still has the panel.
type platformAdminGrant struct {
	// Authority is which claim admitted the sign-in. Today the per-product
	// grant is the only one that does, so this is always "product_admin";
	// it is reported rather than assumed so that a second administrator
	// claim, when the contract grows one, is distinguishable in an audit
	// line without changing what this one means.
	Authority string
	// Role is the desk role, when the administrator also happens to work the
	// desk. Audit only, and empty for everybody else — a desk role has not
	// admitted anybody since the per-product grant became the only key.
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
// The window is ARENA_OIDC_SESSION_TTL_HOURS, which is now what that variable
// is for: it used to bound an Arena-operated admin SSO session as well, and
// that flow is retired. When the grant lapses the person stays signed in as a
// customer; only the admin authority goes.
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
	return &platformAdminGrant{Authority: admin.authority(), Role: admin.Role, GrantedAt: now, ExpiresAt: expires}
}

// claimIsTrue applies the presence rule to one claim.
//
// Anything that is not literally `true` — absent, null, false, a string, a
// number — is "no". Presence of the wrong shape is not evidence of anything,
// and this is the direction it has to fail in.
func claimIsTrue(claims map[string]json.RawMessage, name string) bool {
	raw, present := claims[name]
	if !present {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

// platformAdminFromVerifiedClaims applies the presence rule to the claim set
// of an already-verified token: `product_admin: true` admits, and nothing
// else does. `staff: true` is read too, but only so the sign-in can be
// recorded as a desk identity — it decides nothing.
func platformAdminFromVerifiedClaims(claims map[string]json.RawMessage) verifiedPlatformAdmin {
	staff := claimIsTrue(claims, platformAdminStaffClaim)
	productAdmin := claimIsTrue(claims, platformAdminProductClaim)
	role := ""
	if staff {
		if rawRole, ok := claims[platformAdminRoleClaim]; ok {
			// A role that is not a string is dropped rather than rejected:
			// the vocabulary is open and the role is audit detail either way.
			// It is only ever read beside `staff` — it describes the desk,
			// and a product grant has no roles.
			var decoded string
			if err := json.Unmarshal(rawRole, &decoded); err == nil {
				role = strings.TrimSpace(decoded)
			}
		}
	}
	return verifiedPlatformAdmin{Present: productAdmin, Staff: staff, ProductAdmin: productAdmin, Role: role}
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
	grant, ok := s.platformAdminGrantAt(now)
	if !ok {
		return "", false
	}
	return grant.Role, true
}

// platformAdminGrantAt is platformAdminAt with the whole grant: which claim
// admitted the sign-in as well as the role, for the principal and the panel.
func (s *CustomerSession) platformAdminGrantAt(now time.Time) (platformAdminGrant, bool) {
	if s == nil || s.platformAdmin == nil {
		return platformAdminGrant{}, false
	}
	if !now.Before(s.platformAdmin.ExpiresAt) {
		return platformAdminGrant{}, false
	}
	return *s.platformAdmin, true
}

// platformAdminPrincipalName is the audit identity for one admitted session.
//
// The account id, and not the address — the same rule the sign-in log line
// follows. Every admitted session holds a per-product grant, so every
// principal reads `accounts-product-admin:<account_id>`; where that person
// also works the desk their role is appended, which is audit detail about who
// acted and never about what let them in.
func platformAdminPrincipalName(accountID string, grant platformAdminGrant) string {
	principal := "accounts-product-admin:" + accountID
	if grant.Role != "" {
		principal += ":" + grant.Role
	}
	return principal
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
 * a browser attaches it to everything on its own. The retired admin SSO cookie
 * was held to less, and could afford to be: it was only ever sent to the admin
 * app. This one is sent from every ordinary page of the site, so the panel
 * sends the session's CSRF token with each mutation (see
 * AdminSessionInfoHandler) and a request that does not is refused.
 */
func (h *CustomerOIDCHandler) platformAdminPrincipal(r *http.Request) (principal string, authorized bool, denyReason string) {
	if h == nil {
		return "", false, ""
	}
	session := h.GetSession(r)
	if session == nil {
		return "", false, ""
	}
	grant, ok := session.platformAdminGrantAt(time.Now())
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
	return platformAdminPrincipalName(session.AccountID, grant), true, ""
}

/*
 * adminPanelPath returns the Admin Panel a request belongs to, honoring
 * whichever prefix it arrived on. The router mirrors /admin/* under
 * /arena/admin/* for prefixed deployments, so a hardcoded "/admin/" sends an
 * /arena/-mounted visitor to the wrong app.
 */
func adminPanelPath(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/arena/") {
		return "/arena/admin/"
	}
	return "/admin/"
}

/*
 * AdminSessionInfoHandler is what the Admin Panel reads before it draws
 * anything, and it is answered entirely by the Angel Accounts per-product
 * administrator grant.
 *
 * The panel used to ask this of an Arena-operated admin SSO application with
 * its own cookie and its own email allowlist. Both are retired: a human
 * administrator signs in at Accounts like any other customer, and the claims
 * on that sign-in are what this reports. Nothing here is authority — the
 * admin routes ask platformAdminPrincipal the same question for themselves —
 * it only tells the browser whether to draw the panel or the sign-in button,
 * and `authority` says which claim opened it.
 *
 * csrf_token is handed over because the panel's own mutations travel on the
 * customer cookie and are held to the CSRF check in platformAdminPrincipal.
 * It is the same token /api/v1/account/session already returns to the same
 * browser, and it is only ever returned to a request that already carries the
 * session it belongs to.
 */
func (h *CustomerOIDCHandler) AdminSessionInfoHandler(w http.ResponseWriter, r *http.Request) {
	setCustomerNoStore(w)
	loginEnabled := customerAccountAuthEnabled(h)
	/*
	 * signedIn separates "nobody is here" from "somebody is here who may not
	 * come in", which is the difference between offering a sign-in and
	 * explaining why signing in again will not help. It is not a leak: the
	 * browser holding the cookie can already read the same fact from
	 * /api/v1/account/session, and nothing about the grant is disclosed —
	 * only that this session does not hold one.
	 */
	unauthenticated := func(signedIn bool) {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			"signed_in":     signedIn,
			"login_enabled": loginEnabled,
			"login_url":     customerAPIDashboardPath(r, "/login") + "?return_to=" + url.QueryEscape(adminPanelPath(r)),
		})
	}
	if h == nil {
		unauthenticated(false)
		return
	}
	session := h.GetSession(r)
	if session == nil {
		unauthenticated(false)
		return
	}
	h.refreshSessionCookie(w, r, session)
	grant, isAdmin := session.platformAdminGrantAt(time.Now())
	if !isAdmin {
		/*
		 * Signed in, but holding no Arena grant. Reported as "not
		 * authenticated" on purpose: as far as the Admin Panel is concerned
		 * that is the whole truth, and the panel has nothing else to offer.
		 * A desk identity lands here too, and that is the point — working the
		 * desk is not administering Arena. Signing out first is not required:
		 * signing in again once the grant exists re-reads the claims.
		 *
		 * `signed_in` is what stops that being a dead end. Somebody who has
		 * just completed a sign-in and arrived back here needs to be told that
		 * the sign-in worked and the grant is what is missing; without it the
		 * panel can only offer the same button again.
		 */
		unauthenticated(true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"signed_in":     true,
		"login_enabled": loginEnabled,
		// Which claim admitted this sign-in — "product_admin", the only one
		// that does. Named for audit, exactly as the principal is. Nothing
		// branches on it.
		"authority": grant.Authority,
		// The desk role, when this administrator also works the desk and
		// Accounts named one. Empty for everybody else, and never authority.
		"role": grant.Role,
		"name": session.Name,
		// The account id, never an address — the same rule the sign-in log
		// line and the admin principal follow.
		"account_id": session.AccountID,
		"csrf_token": session.CSRFToken,
		"expires_at": session.ExpiresAt,
		"logout_url": customerAPIDashboardPath(r, "/logout"),
	})
}

// AdminSessionUnavailableHandler answers the Admin Panel's bootstrap when
// there is no customer sign-in configured at all. The panel then has only the
// machine paths to offer, which is the honest answer.
func AdminSessionUnavailableHandler(w http.ResponseWriter, _ *http.Request) {
	setCustomerNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": false,
		"signed_in":     false,
		"login_enabled": false,
	})
}
