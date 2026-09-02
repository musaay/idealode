package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// cookieSession, anonim oturum çerezinin adı. Giriş yoktur: kimlik yalnız
// bu rastgele kimliktir ve sohbet geçmişini kullanıcıya bağlar.
const cookieSession = "sid"

// sessionIDLen, oturum kimliğinin hex uzunluğu (32 bayt = 64 hex karakter).
const sessionIDLen = 64

// sessionCookieMaxAge, oturum çerezinin ömrü (1 yıl).
const sessionCookieMaxAge = 365 * 24 * 60 * 60

// sessionCtxKey, ctx'te oturum kimliğini taşıyan anahtar tipi. Dışa
// kapalıdır: başka paket aynı anahtarı üretemez.
type sessionCtxKey struct{}

// SessionFromContext, isteğe ait anonim oturum kimliğini döner; yoksa boş.
// apiclient bu değeri `X-Session-Id` başlığına yazar.
func SessionFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sid, _ := ctx.Value(sessionCtxKey{}).(string)
	return sid
}

// WithSession, oturum kimliğini ctx'e koyar. Dışa açıktır: kimliği web
// yazar, apiclient SessionFromContext ile okur.
func WithSession(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, sid)
}

// newSessionID, 32 baytlık kriptografik rastgele kimlik üretir.
// crypto/rand.Read Go 1.24'ten beri hata dönmez (hata durumunda süreç
// çöker), yine de dönüş değeri savunmacı biçimde yok sayılmaz.
func newSessionID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Rastgelelik kaynağı yoksa uydurma kimlik üretmek yerine boş
		// dönülür: çağıran çerez yazmaz, sohbet 400 ile reddedilir.
		return ""
	}
	return hex.EncodeToString(b[:])
}

// validSessionID, çerezden gelen değeri doğrular: yalnız 64 hex karakter.
// Biçimsiz değer (elle düzenlenmiş çerez) yenisiyle değiştirilir.
func validSessionID(s string) bool {
	if len(s) != sessionIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// sessionMiddleware, her isteğe anonim oturum kimliği bağlar: çerez varsa
// okunur, yoksa üretilip yazılır. Çerez HttpOnly'dir — JS'in okumasına gerek
// yoktur, sohbet istekleri hep sunucudan geçer.
func sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := ""
		if c, err := r.Cookie(cookieSession); err == nil && validSessionID(c.Value) {
			sid = c.Value
		}
		if sid == "" {
			if sid = newSessionID(); sid != "" {
				http.SetCookie(w, &http.Cookie{
					Name:     cookieSession,
					Value:    sid,
					Path:     "/",
					MaxAge:   sessionCookieMaxAge,
					HttpOnly: true,
					Secure:   isHTTPS(r),
					SameSite: http.SameSiteLaxMode,
				})
			}
		}
		next.ServeHTTP(w, r.WithContext(WithSession(r.Context(), sid)))
	})
}
