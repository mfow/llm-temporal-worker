package provider

import "github.com/mfow/llm-temporal-worker/llm"

type CompileInput struct {
	Request    llm.Request
	Query      CapabilityQuery
	Capability CapabilitySet
	Strict     bool
	Metadata   CallMetadata
}

type Call struct {
	EndpointID   string
	Family       Family
	Model        string
	ServiceClass llm.ServiceClass
	SDKParams    any
	Metadata     CallMetadata
}

type CallMetadata struct {
	SchemaDigest        [32]byte
	EstimatedBytes      int
	CapabilityVersion   string
	ProviderTier        string
	OpaqueStateRequired bool
}

type Result struct {
	Response llm.Response
}
