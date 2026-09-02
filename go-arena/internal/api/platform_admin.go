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
 * administrators"): two claims admit, and either one is enough.
 *
 *   - `staff: true` (with `staff_role` for audit) marks the support desk —
 *     the identities that administer every Angel product.
 *   - `product_admin: true` marks a per-person, per-product grant: somebody
 *     the desk has made an administrator of Arena specifically. Accounts
 *     computes it against the product the token was minted for, so on a
 *     token Arena verified it can only mean Arena.
 *   - Everybody else carries neither claim. There is deliberately no
 *     `staff: false` or `product_admin: false`, so the question Arena asks
 *     is whether a claim is PRESENT and true — never whether some value is
 *     truthy, and never anything about an address, an account name, or any
 *     other signal.
 *   - Both are computed when the token is minted. They are facts about that
 *     sign-in, not properties of the account, so they are never written down.
 *   - `staff_role` is an open vocabulary. An unrecognised value means "an
 *     administrator, nothing finer" — not "not an administrator", and not a
 *     reason to guess at a permission level Accounts did not describe.
 *
 * Administering the platform is orthogonal to owning things on it. Nothing
 * here touches an entitlement check; see customer_entitlements.go.
 */

const (
	platformAdminStaffClaim   = "staff"
	platformAdminRoleClaim    = "staff_role"
	platformAdminProductClaim = "product_admin"
)

// The two ways a sign-in can carry administrator authority, named the way
// the principal and the session-info endpoint report them.
const (
	platformAdminAuthorityStaff   = "staff"
	platformAdminAuthorityProduct = "product_admin"
)

// verifiedPlatformAdmin is what one validated id_token said about whether the
// person signing in administers the platform.
//
// Present is the whole answer. Staff and ProductAdmin record which claim (or
// claims) said so — for the audit principal and the panel, never for a
// permission decision: both admit to exactly the same routes. Role is carried
// for audit and for a future that distinguishes roles; today nothing branches
// on it, because the contract says an unrecognised role is still an
// administrator and Arena has no business inventing the finer grades.
type verifiedPlatformAdmin struct {
	Present      bool
	Staff        bool
	ProductAdmin bool
	Role         string
}

// authority names the claim that admitted this sign-in. The desk claim is
// reported when both are present: it is the wider authority, and the
// per-product grant adds nothing to it.
func (a verifiedPlatformAdmin) authority() string {
	switch {
	case a.Staff:
		return platformAdminAuthorityStaff
	case a.ProductAdmin:
		return platformAdminAuthorityProduct
	default:
		return ""
	}
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
	// Authority is which claim admitted the sign-in: "staff" or
	// "product_admin". Bounded by the same window either way.
	Authority string
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
// of an already-verified token: `staff: true` or `product_admin: true`
// admits, and nothing else does.
func platformAdminFromVerifiedClaims(claims map[string]json.RawMessage) verifiedPlatformAdmin {
	staff := claimIsTrue(claims, platformAdminStaffClaim)
	productAdmin := claimIsTrue(claims, platformAdminProductClaim)
	if !staff && !productAdmin {
		return verifiedPlatformAdmin{}
	}
	role := ""
	if staff {
		if rawRole, ok := claims[platformAdminRoleClaim]; ok {
			// A role that is not a string is dropped rather than rejected:
			// the vocabulary is open and the administrator answer has already
			// been given by `staff`. A role is only ever read beside `staff`
			// — it describes the desk, and a product grant has no roles.
			var decoded string
			if err := json.Unmarshal(rawRole, &decoded); err == nil {
				role = strings.TrimSpace(decoded)
			}
		}
	}
	return verifiedPlatformAdmin{Present: true, Staff: staff, ProductAdmin: productAdmin, Role: role}
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
// follows. A desk sign-in is `accounts-staff:<account_id>[:role]`, with the
// role appended when Accounts named one; a per-product grant is
// `accounts-product-admin:<account_id>`, so an operator reading an audit row
// can tell which kind of authority acted without looking anything up.
func platformAdminPrincipalName(accountID string, grant platformAdminGrant) string {
	if grant.Authority == platformAdminAuthorityProduct {
		return "accounts-product-admin:" + accountID
	}
	principal := "accounts-staff:" + accountID
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
 * anything, and it is answered entirely by the Angel Accounts administrator
 * claims — the desk role or the per-product grant.
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
	unauthenticated := func() {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			"login_enabled": loginEnabled,
			"login_url":     customerAPIDashboardPath(r, "/login") + "?return_to=" + url.QueryEscape(adminPanelPath(r)),
		})
	}
	if h == nil {
		unauthenticated()
		return
	}
	session := h.GetSession(r)
	if session == nil {
		unauthenticated()
		return
	}
	h.refreshSessionCookie(w, r, session)
	grant, isAdmin := session.platformAdminGrantAt(time.Now())
	if !isAdmin {
		/*
		 * Signed in, but with neither claim. This is reported as "not
		 * authenticated" on purpose: as far as the Admin Panel is concerned
		 * that is the whole truth, and the panel has nothing else to offer a
		 * customer. Signing out first is not required — signing in again once
		 * the desk role or the product grant exists re-reads the claims.
		 */
		unauthenticated()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"login_enabled": loginEnabled,
		// Which claim admitted this sign-in: "staff" or "product_admin".
		// Named for audit, exactly as the principal is. Nothing branches on it.
		"authority": grant.Authority,
		// The desk role, when Accounts named one; empty for a product grant.
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
		"login_enabled": false,
	})
}
