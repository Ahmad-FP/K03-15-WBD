package session

import (
	"context"
	"crypto/rand"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	CookieName  = "session"
	IdleTimeout = 30 * time.Minute
	MaxAge      = 24 * time.Hour
	RotateEvery = 15 * time.Minute
	Grace       = 60 * time.Second
)

type Session struct {
	ID       string
	UserID   int64
	Username string
	Admin    bool
}

type Store struct {
	DB *pgxpool.Pool
}

type key struct{}

func Current(r *http.Request) Session {
	return r.Context().Value(key{}).(Session)
}

func (s *Store) Start(w http.ResponseWriter, r *http.Request, userID int64) {
	id := rand.Text()
	s.DB.Exec(r.Context(), "INSERT INTO sessions (id, user_id, created_at, last_seen_at, rotated_at) VALUES ($1, $2, $3, $3, $3)", id, userID, time.Now())
	setCookie(w, r, id, 0)
}

func (s *Store) End(w http.ResponseWriter, r *http.Request) {
	s.DB.Exec(r.Context(), "DELETE FROM sessions WHERE id = $1", Current(r).ID)
	setCookie(w, r, "", -1)
}

func (s *Store) RotateUser(ctx context.Context, userID int64) {
	s.DB.Exec(ctx, "UPDATE sessions SET rotated_at = 'epoch' WHERE user_id = $1", userID)
}

func (s *Store) RotateGroup(ctx context.Context, groupID int64) {
	s.DB.Exec(ctx, "UPDATE sessions SET rotated_at = 'epoch' WHERE user_id IN (SELECT user_id FROM group_members WHERE group_id = $1)", groupID)
}

func (s *Store) RevokeUser(ctx context.Context, userID int64) {
	s.DB.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", userID)
}

// logout paksa semua anggota group, dipakai waktu akses group dicabut
func (s *Store) RevokeGroup(ctx context.Context, groupID int64) {
	s.DB.Exec(ctx, "DELETE FROM sessions WHERE user_id IN (SELECT user_id FROM group_members WHERE group_id = $1)", groupID)
}

func (s *Store) Require(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.resolve(w, r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), key{}, sess)))
	})
}

func (s *Store) resolve(w http.ResponseWriter, r *http.Request) (Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, false
	}
	now := time.Now()
	var sess Session
	var created, seen, rotated time.Time
	err = s.DB.QueryRow(r.Context(), `SELECT s.id, s.created_at, s.last_seen_at, s.rotated_at, u.id, u.username, u.is_admin
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 OR (s.prev_id = $1 AND s.prev_until > $2)`, c.Value, now).
		Scan(&sess.ID, &created, &seen, &rotated, &sess.UserID, &sess.Username, &sess.Admin)
	valid, rotate := check(created, seen, rotated, now)
	if err != nil || !valid {
		return Session{}, false
	}
	if rotate && sess.ID == c.Value {
		s.DB.Exec(r.Context(), "UPDATE sessions SET prev_id = id, prev_until = $3, id = $2, rotated_at = $4, last_seen_at = $4 WHERE id = $1", c.Value, rand.Text(), now.Add(Grace), now)
		s.DB.QueryRow(r.Context(), "SELECT id FROM sessions WHERE prev_id = $1", c.Value).Scan(&sess.ID)
	} else {
		s.DB.Exec(r.Context(), "UPDATE sessions SET last_seen_at = $2 WHERE id = $1", sess.ID, now)
	}
	if sess.ID != c.Value {
		setCookie(w, r, sess.ID, 0)
	}
	return sess, true
}

func check(created, seen, rotated, now time.Time) (valid, rotate bool) {
	valid = now.Sub(created) < MaxAge && now.Sub(seen) < IdleTimeout
	return valid, valid && now.Sub(rotated) >= RotateEvery
}

func setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}
