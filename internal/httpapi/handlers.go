package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"rumarasa-backend/internal/auth"
	"rumarasa-backend/internal/store"
)

const refreshCookie = "rumarasa_refresh"

// ---------- Auth ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil ||
		body.Username == "" || body.Password == "" {
		respondError(w, http.StatusBadRequest, "validation", "username and password are required")
		return
	}

	admin, err := s.store.GetAdminByUsername(r.Context(), body.Username)
	if errors.Is(err, store.ErrNotFound) {
		// Burn comparable time so unknown usernames aren't distinguishable.
		bcrypt.CompareHashAndPassword([]byte("$2a$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinva"), []byte(body.Password))
		respondError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(body.Password)) != nil {
		respondError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
		return
	}

	if err := s.issueSession(w, r, admin.ID); err != nil {
		respondInternal(w, err)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookie)
	if err != nil || cookie.Value == "" {
		respondError(w, http.StatusUnauthorized, "unauthorized", "Missing refresh token")
		return
	}
	adminID, err := s.store.ConsumeRefreshToken(r.Context(), auth.HashToken(cookie.Value))
	if errors.Is(err, store.ErrNotFound) {
		s.clearRefreshCookie(w)
		respondError(w, http.StatusUnauthorized, "unauthorized", "Invalid refresh token")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	if err := s.issueSession(w, r, adminID); err != nil {
		respondInternal(w, err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshCookie); err == nil && cookie.Value != "" {
		// Best-effort revoke; the cookie is cleared regardless.
		if _, err := s.store.ConsumeRefreshToken(r.Context(), auth.HashToken(cookie.Value)); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			slog.Error("revoke refresh token on logout", "err", err)
		}
	}
	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// issueSession rotates the refresh cookie and returns a fresh access token.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, adminID int) error {
	accessToken, err := auth.NewAccessToken(s.cfg.JWTSecret, adminID)
	if err != nil {
		return err
	}
	refreshToken, tokenHash, err := auth.NewRefreshToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(auth.RefreshTokenTTL)
	if err := s.store.InsertRefreshToken(r.Context(), tokenHash, adminID, expiresAt); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    refreshToken,
		Path:     "/api/v1/auth",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   s.cfg.IsProd(),
		SameSite: http.SameSiteStrictMode,
	})
	respondData(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"expires_in":   int(auth.AccessTokenTTL.Seconds()),
	})
	return nil
}

func (s *Server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    "",
		Path:     "/api/v1/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.IsProd(),
		SameSite: http.SameSiteStrictMode,
	})
}

// ---------- Content blocks ----------

var contentKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,199}$`)

func (s *Server) handleListContent(w http.ResponseWriter, r *http.Request) {
	content, err := s.store.ListContent(r.Context())
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, content)
}

func (s *Server) handleUpsertContent(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !contentKeyRe.MatchString(key) {
		respondError(w, http.StatusBadRequest, "validation", "invalid content key (lowercase letters, digits, . _ - only)")
		return
	}
	var body struct {
		Value *string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.Value == nil {
		respondError(w, http.StatusBadRequest, "validation", `body must be {"value": "..."}`)
		return
	}
	block, err := s.store.UpsertContent(r.Context(), key, *body.Value)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, block)
}

func (s *Server) handleDeleteContent(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteContent(r.Context(), r.PathValue("key"))
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Content key not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Collections ----------

func (s *Server) collection(w http.ResponseWriter, r *http.Request) (store.Collection, bool) {
	c, ok := store.CollectionByPath(r.PathValue("collection"))
	if !ok {
		respondError(w, http.StatusNotFound, "not_found", "Unknown collection")
	}
	return c, ok
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	c, ok := s.collection(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListItems(r.Context(), c)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, items)
}

func (s *Server) decodeItem(w http.ResponseWriter, r *http.Request, c store.Collection, isCreate bool) (map[string]any, bool) {
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return nil, false
	}
	clean, err := c.ValidateItem(body, isCreate)
	if err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return nil, false
	}
	return clean, true
}

func (s *Server) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	c, ok := s.collection(w, r)
	if !ok {
		return
	}
	item, ok := s.decodeItem(w, r, c, true)
	if !ok {
		return
	}
	created, err := s.store.CreateItem(r.Context(), c, item)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusCreated, created)
}

func itemID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		respondError(w, http.StatusBadRequest, "validation", "invalid id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleUpdateItem(w http.ResponseWriter, r *http.Request) {
	c, ok := s.collection(w, r)
	if !ok {
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	item, ok := s.decodeItem(w, r, c, false)
	if !ok {
		return
	}
	updated, err := s.store.UpdateItem(r.Context(), c, id, item)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	c, ok := s.collection(w, r)
	if !ok {
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteItem(r.Context(), c, id)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Images ----------

const maxImageBytes = 5 << 20 // 5 MB

var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+4096)
	file, header, err := r.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			respondError(w, http.StatusRequestEntityTooLarge, "too_large", "Image exceeds the 5 MB limit")
			return
		}
		respondError(w, http.StatusBadRequest, "validation", `multipart field "file" is required`)
		return
	}
	defer file.Close()

	data, contentType, ok := readImage(w, file)
	if !ok {
		return
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		respondInternal(w, err)
		return
	}
	id := hex.EncodeToString(idBytes)

	meta, err := s.store.CreateImage(r.Context(), id, header.Filename, contentType, data)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusCreated, map[string]any{
		"id":  meta.ID,
		"url": "/api/v1/images/" + meta.ID,
	})
}

// readImage buffers the upload and sniffs its real content type — the
// client-supplied type is untrusted.
func readImage(w http.ResponseWriter, file multipart.File) ([]byte, string, bool) {
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	if err != nil {
		respondInternal(w, err)
		return nil, "", false
	}
	if len(data) > maxImageBytes {
		respondError(w, http.StatusRequestEntityTooLarge, "too_large", "Image exceeds the 5 MB limit")
		return nil, "", false
	}
	contentType := http.DetectContentType(data)
	if _, ok := allowedImageTypes[contentType]; !ok {
		respondError(w, http.StatusUnsupportedMediaType, "unsupported_type", "Only JPEG, PNG, WebP, or GIF images are allowed")
		return nil, "", false
	}
	return data, contentType, true
}

func (s *Server) handleGetImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	contentType, data, err := s.store.GetImage(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Image not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	// Image ids are immutable — replacing an image means uploading a new one —
	// so browsers may cache aggressively.
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.store.ListImages(r.Context())
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, images)
}

func (s *Server) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteImage(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Image not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
