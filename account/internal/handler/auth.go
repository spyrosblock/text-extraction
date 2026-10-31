package handler

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/spyrosblock/text-extraction/account/internal/email"
	"github.com/spyrosblock/text-extraction/billing"
)

// Sign-in rate limits. Over them, /api/auth/start still answers 202 but
// sends nothing.
const (
	startPerEmail       = 3
	startPerEmailWindow = 15 * time.Minute
	startPerIP          = 20
	startPerIPWindow    = time.Hour
)

// maxEmail is the longest address accepted.
const maxEmail = 254

// Start serves POST /api/auth/start {"email"}: it emails a sign-in link. The
// answer is 202 whether or not the address has an account, is rate limited
// or the email could be sent, so it can't be used to probe accounts; 400
// only for a malformed address.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	addr := billing.NormalizeEmail(req.Email)
	if !validEmail(addr) {
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}
	h.sendLink(r.Context(), addr, clientIP(r))
	w.WriteHeader(http.StatusAccepted)
}

// sendLink emails a sign-in link unless a rate limit is reached. Failures
// are only logged.
func (h *Handler) sendLink(ctx context.Context, addr, ip string) {
	// Addresses stay out of the logs and blob names; their hash identifies them.
	emailHash := billing.EmailHash(addr)
	log := slog.With("emailHash", emailHash, "ip", ip)
	for _, l := range []struct {
		kind, value string
		limit       int64
		window      time.Duration
	}{
		{"ip", ip, startPerIP, startPerIPWindow},
		{"email", emailHash, startPerEmail, startPerEmailWindow},
	} {
		ok, err := h.Limits.Allow(ctx, l.kind, l.value, l.limit, l.window)
		if err != nil {
			log.ErrorContext(ctx, "sign-in rate limit", "error", err)
			return
		}
		if !ok {
			log.WarnContext(ctx, "sign-in rate limited", "by", l.kind)
			return
		}
	}

	token, err := h.LoginTokens.Create(ctx, addr)
	if err != nil {
		log.ErrorContext(ctx, "create login token", "error", err)
		return
	}
	// The token is in the fragment: browsers don't send it to the server
	// or in a Referer.
	link := strings.TrimSuffix(h.SiteURL, "/") + "/auth.html#token=" + token
	if err := h.Email.Send(ctx, signInMessage(addr, link)); err != nil {
		log.ErrorContext(ctx, "send sign-in email", "error", err)
		return
	}
	log.InfoContext(ctx, "sign-in link sent")
}

func signInMessage(to, link string) email.Message {
	const ttl = "15 minutes"
	return email.Message{
		To:      to,
		Subject: "Your sign-in link",
		Text: fmt.Sprintf("Sign in to text-extraction with this link, valid for %s:\n\n%s\n\n"+
			"If you didn't ask to sign in, ignore this email.\n", ttl, link),
		HTML: fmt.Sprintf(`<p>Sign in to text-extraction with this link, valid for %s:</p>`+
			`<p><a href="%s">Sign in</a></p>`+
			`<p>If you didn't ask to sign in, ignore this email.</p>`, ttl, html.EscapeString(link)),
	}
}

// validEmail checks the shape of a normalized address: one @, something
// before it, a dot inside the domain, no spaces or control characters.
func validEmail(addr string) bool {
	if len(addr) > maxEmail || strings.Count(addr, "@") != 1 {
		return false
	}
	if strings.ContainsFunc(addr, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	local, domain, _ := strings.Cut(addr, "@")
	dot := strings.Index(domain, ".")
	return local != "" && dot > 0 && !strings.HasSuffix(domain, ".")
}

type verifyResponse struct {
	Session string `json:"session"`
	Email   string `json:"email"`
}

// Verify serves POST /api/auth/verify {"token"}: it consumes a sign-in
// token, finds or creates the address's account and starts a session. 401
// for an unknown, used or expired token.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	addr, err := h.LoginTokens.Consume(ctx, req.Token)
	if errors.Is(err, billing.ErrInvalidToken) {
		writeError(w, http.StatusUnauthorized, "this link has expired or was already used")
		return
	}
	if err != nil {
		unexpected(w, r, "check sign-in link", err)
		return
	}
	id, acc, err := h.Accounts.FindOrCreate(ctx, addr)
	if err != nil {
		unexpected(w, r, "find account", err)
		return
	}
	token, err := h.Sessions.Create(ctx, id)
	if err != nil {
		unexpected(w, r, "create session", err)
		return
	}
	slog.InfoContext(ctx, "signed in", "accountId", id)
	writeJSON(w, http.StatusOK, verifyResponse{Session: token, Email: acc.Email})
}

// Logout serves POST /api/auth/logout: it ends the caller's session.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	h.authed(func(w http.ResponseWriter, r *http.Request, s session) {
		if err := h.Sessions.Delete(r.Context(), s.token); err != nil {
			unexpected(w, r, "sign out", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})(w, r)
}
