package routing

const (
	RejectTenant       = "route_tenant_not_allowed"
	RejectRegion       = "route_region_not_allowed"
	RejectModel        = "route_model_mismatch"
	RejectHealth       = "route_health_open"
	RejectCapability   = "route_capability_unavailable"
	RejectPrice        = "route_price_missing"
	RejectExtension    = "route_extension_unsupported"
	RejectContext      = "route_context_limit"
	RejectContinuation = "continuation_pinned"
	RejectClass        = "route_service_class_unavailable"
	RejectInvalid      = "route_invalid"
)
