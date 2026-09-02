package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"arena-server/internal/db"

	"github.com/go-chi/chi/v5"
)

const (
	profileDisplayNameMaxRunes = 32
	profileBioMaxRunes         = 280
	profileAvatarColorMaxLen   = 32
)

// chatDisplayHandle mirrors ws.chatHandle: the sanitized display name plus an
// 8-hex-char discriminator hashed from the account id. Duplicated (rather
// than exported from the ws package) to keep the api package from importing
// ws just for a five-line string formula.
// profileAvatarColorPattern is a CSS hex colour (#rgb, #rgba, #rrggbb,
// #rrggbbaa) or empty, which clears the colour.
var profileAvatarColorPattern = regexp.MustCompile(`^(#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8}))?$`)

func chatDisplayHandle(accountID, name string) string {
	clean, _ := sanitizeProfileText(name, 24)
	if clean == "" {
		clean = "dev"
	}
	sum := sha256.Sum256([]byte(accountID))
	return clean + "#" + hex.EncodeToString(sum[:])[:8]
}

// sanitizeProfileText strips control/format characters and collapses
// whitespace, matching the chat body sanitizer's rules so a profile field
// cannot be used to smuggle the same spoofing tricks (zero-width characters,
// bidi overrides) chat already guards against.
func sanitizeProfileText(raw string, maxRunes int) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	text := strings.TrimSpace(b.String())
	if maxRunes > 0 && utf8.RuneCountInString(text) > maxRunes {
		return "", false
	}
	return text, true
}

func profileJSON(p *db.PublicProfile) map[string]interface{} {
	return map[string]interface{}{
		"account_id":   p.AccountID,
		"display_name": p.DisplayName,
		"chat_handle":  chatDisplayHandle(p.AccountID, p.DisplayName),
		"bio":          p.Bio,
		"avatar_color": p.AvatarColor,
		"joined_at":    p.JoinedAt,
		"shows_bots":   p.ShowsBots,
		"bots":         p.Bots,
	}
}

// PublicProfileHandler serves GET /api/v1/profile/{account_id}. Public and
// unauthenticated, same trust level as the chat feed itself: account ids
// only become discoverable by reading messages in the (already public) chat
// stream, and this endpoint never returns an email address.
func PublicProfileHandler(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(chi.URLParam(r, "account_id"))
	if accountID == "" || len(accountID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid account id")
		return
	}
	profile, err := db.GetPublicProfile(r.Context(), accountID)
	if err != nil {
		if errors.Is(err, db.ErrNoDatabase) {
			writeError(w, http.StatusServiceUnavailable, "profiles require the database")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load profile")
		return
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	writeJSON(w, http.StatusOK, profileJSON(profile))
}

// UpdateAccountProfileHandler serves PATCH /api/v1/account/profile, mounted
// behind MakeCustomerAuthMiddleware (session + same-origin + CSRF) alongside
// the other /account/* routes.
func UpdateAccountProfileHandler(w http.ResponseWriter, r *http.Request) {
	session := CustomerSessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "customer authentication required")
		return
	}
	var req struct {
		DisplayName    *string `json:"display_name"`
		Bio            *string `json:"bio"`
		AvatarColor    *string `json:"avatar_color"`
		ShowBotsPublic *bool   `json:"show_bots_public"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	update := db.CustomerProfileUpdate{ShowBotsPublic: req.ShowBotsPublic}
	if req.DisplayName != nil {
		clean, ok := sanitizeProfileText(*req.DisplayName, profileDisplayNameMaxRunes)
		if !ok || clean == "" {
			writeError(w, http.StatusBadRequest, "display name must be 1-32 characters")
			return
		}
		update.DisplayName = &clean
	}
	if req.Bio != nil {
		clean, ok := sanitizeProfileText(*req.Bio, profileBioMaxRunes)
		if !ok {
			writeError(w, http.StatusBadRequest, "bio must be at most 280 characters")
			return
		}
		update.Bio = &clean
	}
	if req.AvatarColor != nil {
		color := strings.TrimSpace(*req.AvatarColor)
		// A hex colour or nothing. Every consumer puts this inside a CSS
		// `background:` declaration, and the length check alone let a
		// declaration terminator through; the profile popup already insists
		// on hex, so the server now states the same contract.
		if len(color) > profileAvatarColorMaxLen || !profileAvatarColorPattern.MatchString(color) {
			writeError(w, http.StatusBadRequest, "avatar color must be a hex colour like #22ccff")
			return
		}
		update.AvatarColor = &color
	}

	profile, err := db.UpdateCustomerProfile(r.Context(), session.AccountID, update)
	if err != nil {
		if errors.Is(err, db.ErrNoDatabase) {
			writeError(w, http.StatusServiceUnavailable, "profiles require the database")
			return
		}
		if errors.Is(err, db.ErrCustomerAccountNotFound) {
			writeError(w, http.StatusNotFound, "account not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update profile")
		return
	}
	// The session cache copied the old display name at sign-in and chat
	// posts under it; drop the copies so the next request reads the row.
	activeCustomerOIDCHandler().ForgetAccountSessions(session.AccountID)
	writeJSON(w, http.StatusOK, profileJSON(profile))
}
