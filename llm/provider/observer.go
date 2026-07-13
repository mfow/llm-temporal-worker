package provider

import "context"

type ResponseMetadata struct {
	RequestID    string
	ResponseID   string
	ProviderTier string
	Status       int
}

type Progress struct {
	Phase       string
	OutputItems int
}

type Observer interface {
	BeforePossibleWrite(context.Context) error
	AfterResponseHeaders(context.Context, ResponseMetadata) error
	OnProgress(context.Context, Progress)
}

type NopObserver struct{}

func (NopObserver) BeforePossibleWrite(context.Context) error { return nil }
func (NopObserver) AfterResponseHeaders(context.Context, ResponseMetadata) error {
	return nil
}
func (NopObserver) OnProgress(context.Context, Progress) {}
