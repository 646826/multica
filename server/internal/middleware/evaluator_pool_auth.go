package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
)

type evaluatorTokenCtxKey struct{}

// RequireEvaluatorPoolAuth wraps handlers behind a workspace evaluator-pool token.
// On success, the verified EvaluatorPoolToken is available via EvaluatorTokenFromContext.
func RequireEvaluatorPoolAuth(svc *benchmark.EvaluatorPoolService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := r.Header.Get("Authorization")
			if !strings.HasPrefix(authz, "Bearer ") {
				writeAuthError(w, "unauthenticated")
				return
			}
			tok := strings.TrimPrefix(authz, "Bearer ")
			evp, err := svc.Verify(r.Context(), tok)
			if err != nil {
				writeAuthError(w, "unauthenticated")
				return
			}
			ctx := context.WithValue(r.Context(), evaluatorTokenCtxKey{}, evp)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// EvaluatorTokenFromContext returns the verified token if the request was
// authorized through RequireEvaluatorPoolAuth.
func EvaluatorTokenFromContext(ctx context.Context) (benchmark.EvaluatorPoolToken, bool) {
	v, ok := ctx.Value(evaluatorTokenCtxKey{}).(benchmark.EvaluatorPoolToken)
	return v, ok
}

func writeAuthError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
}
