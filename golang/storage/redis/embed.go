package redis

import (
	_ "embed"
	redisclient "github.com/redis/go-redis/v9"
)

//go:embed functions/admission.lua
var admissionFunctionSource string

//go:embed functions/continuation.lua
var continuationFunctionSource string

var continuationPutScript = redisclient.NewScript(continuationFunctionSource)
