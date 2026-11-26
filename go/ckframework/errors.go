package ckframework

import (
	"net/http"

	"github.com/joomcode/errorx"
)

// Namespace is the root errorx namespace for all corekiosk Go packages.
var Namespace = errorx.NewNamespace("corekiosk")

// Traits classify errors uniformly across packages for HTTP/WS status mapping.
var (
	TraitInvalidInput    = errorx.RegisterTrait("invalid_input")
	TraitUnauthorized    = errorx.RegisterTrait("unauthorized")
	TraitForbidden       = errorx.RegisterTrait("forbidden")
	TraitNotFound        = errorx.RegisterTrait("not_found")
	TraitConflict        = errorx.RegisterTrait("conflict")
	TraitUnavailable     = errorx.RegisterTrait("unavailable")
	TraitTimeout         = errorx.RegisterTrait("timeout")
	TraitUpgradeRequired = errorx.RegisterTrait("upgrade_required")
	TraitInternal        = errorx.RegisterTrait("internal")
)

// Canonical typed error definitions used across ckframework, ckserver, cklauncher, and cktelemetry.
var (
	ErrValidation           = Namespace.NewType("validation", TraitInvalidInput)
	ErrUnauthorized         = Namespace.NewType("unauthorized", TraitUnauthorized)
	ErrOperatorTokenExpired = Namespace.NewType("operator_token_expired", TraitUnauthorized)
	ErrForbidden            = Namespace.NewType("forbidden", TraitForbidden)
	ErrNotFound             = Namespace.NewType("not_found", TraitNotFound)
	ErrConflict             = Namespace.NewType("conflict", TraitConflict)
	ErrRevisionMismatch     = Namespace.NewType("revision_mismatch", TraitConflict)
	ErrBusy                 = Namespace.NewType("busy", TraitConflict)
	ErrUpgradeRequired      = Namespace.NewType("upgrade_required", TraitUpgradeRequired)
	ErrBridgeUnavailable    = Namespace.NewType("bridge_unavailable", TraitUnavailable)
	ErrBridgeTimeout        = Namespace.NewType("bridge_timeout", TraitTimeout)
	ErrStore                = Namespace.NewType("store", TraitInternal)
	ErrInternal             = Namespace.NewType("internal", TraitInternal)
)

// PropCode attaches a machine-readable error code to an errorx error.
var PropCode = errorx.RegisterProperty("code")

// PropDetails attaches structured JSON-serializable details (e.g. field errors) to an errorx error.
var PropDetails = errorx.RegisterProperty("details")

// HTTPStatus maps any error (preferring errorx types & traits) to an HTTP status code,
// a stable machine code, and optional structured details.
func HTTPStatus(err error) (status int, code string, details any) {
	if err == nil {
		return http.StatusOK, "", nil
	}
	if ex := errorx.Cast(err); ex != nil {
		if v, ok := ex.Property(PropDetails); ok {
			details = v
		}
		if v, ok := ex.Property(PropCode); ok {
			if s, ok := v.(string); ok && s != "" {
				code = s
			}
		}
		switch {
		case ex.IsOfType(ErrOperatorTokenExpired):
			return http.StatusUnauthorized, defaultCode(code, "operator_token_expired"), details
		case ex.IsOfType(ErrRevisionMismatch):
			return http.StatusConflict, defaultCode(code, "revision_mismatch"), details
		case ex.IsOfType(ErrBusy):
			return http.StatusConflict, defaultCode(code, "busy"), details
		case ex.IsOfType(ErrUpgradeRequired):
			return http.StatusUpgradeRequired, defaultCode(code, "upgrade_required"), details
		case ex.IsOfType(ErrBridgeUnavailable):
			return http.StatusServiceUnavailable, defaultCode(code, "bridge_unavailable"), details
		case ex.IsOfType(ErrBridgeTimeout):
			return http.StatusGatewayTimeout, defaultCode(code, "bridge_timeout"), details
		case errorx.HasTrait(ex, TraitInvalidInput):
			return http.StatusBadRequest, defaultCode(code, "validation_error"), details
		case errorx.HasTrait(ex, TraitUnauthorized):
			return http.StatusUnauthorized, defaultCode(code, "unauthorized"), details
		case errorx.HasTrait(ex, TraitForbidden):
			return http.StatusForbidden, defaultCode(code, "forbidden"), details
		case errorx.HasTrait(ex, TraitNotFound):
			return http.StatusNotFound, defaultCode(code, "not_found"), details
		case errorx.HasTrait(ex, TraitConflict):
			return http.StatusConflict, defaultCode(code, "conflict"), details
		case errorx.HasTrait(ex, TraitUnavailable):
			return http.StatusServiceUnavailable, defaultCode(code, "unavailable"), details
		case errorx.HasTrait(ex, TraitTimeout):
			return http.StatusGatewayTimeout, defaultCode(code, "timeout"), details
		case errorx.HasTrait(ex, TraitUpgradeRequired):
			return http.StatusUpgradeRequired, defaultCode(code, "upgrade_required"), details
		}
	}
	return http.StatusInternalServerError, defaultCode(code, "internal_error"), details
}

func defaultCode(existing, fallback string) string {
	if existing != "" {
		return existing
	}
	return fallback
}
